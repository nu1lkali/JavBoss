package directory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"javboss/internal/common/logging"
	"javboss/internal/db"
	"javboss/internal/emby"
	"javboss/internal/jav/scrape/cover"
	"javboss/internal/models"
)

// 自动整理相关默认值。
const (
	// defaultAutoProcessCoverPoll 是后台队列的轮询间隔。
	defaultAutoProcessCoverPoll = 30 * time.Second
	// defaultAutoProcessCoverWait 是首轮整理等封面队列排空的上限。
	// 超过上限就带着已有封面先写一轮 NFO，剩下的交给后续轮次。
	defaultAutoProcessCoverWait = 5 * time.Minute
	// autoProcessRoundInterval 是封面还在下载时，相邻两轮整理的最小间隔。
	// 整理本身是全量扫描，太密会持续占用磁盘 IO。
	autoProcessRoundInterval = 5 * time.Minute
	// autoProcessMaxAttempts 限制同一个目录在一次入队周期内被整理的轮数，
	// 避免“某几条永远写不进去”时无限重试。
	autoProcessMaxAttempts = 12
)

// autoProcessEnabledKey 是设置页「自动整理」开关对应的配置键。
//
// 分工：JAVBOSS_AUTO_PROCESS 决定“整理成什么样”（模式，需要重启容器才能改），
// 这个开关决定“现在要不要跑”，改动立即生效，不需要重启。
const autoProcessEnabledKey = "jav_auto_process_enabled"

// skipCoverWaitKey 标记本次整理不等待封面下载。
type skipCoverWaitKey struct{}

// withSkipCoverWait 让整理只使用本地已有封面，缺失的交给后台队列继续下载。
//
// 自动整理必须走这条路：手动整理时单条缺图等 30 秒无所谓，但整库 7000 条
// 每条都等一次就是灾难，而且在队列还在下载时重复触发同步下载没有意义。
func withSkipCoverWait(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipCoverWaitKey{}, true)
}

func skipCoverWait(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	skip, _ := ctx.Value(skipCoverWaitKey{}).(bool)
	return skip
}

// normalizeAutoProcessMode 把配置里的模式名归一化为内部常量。
// 空字符串或未知值都表示“不自动整理”，保持原有手动行为。
func normalizeAutoProcessMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case DirectoryProcessSidecar:
		return DirectoryProcessSidecar
	case DirectoryProcessOrganize:
		return DirectoryProcessOrganize
	case DirectoryProcessOrganizeWithSidecar:
		return DirectoryProcessOrganizeWithSidecar
	default:
		return ""
	}
}

// autoProcessModeFor 返回当前生效的整理模式，空字符串表示不自动整理。
//
// 环境变量给的是“能做到什么”，设置页开关给的是“现在做不做”；两者都允许才执行。
// 开关每次从数据库读，所以网页上关掉后最多一个轮询周期就会停手。
func (m *Manager) autoProcessModeFor(ctx context.Context) string {
	enabled, configured := autoProcessEnabledOverride(ctx)
	return resolveAutoProcessMode(m.autoProcessMode, enabled, configured)
}

// resolveAutoProcessMode 把环境变量里的模式与设置页开关合并成最终模式。
//
// configured=false 表示配置表里没有这个键（老库升级、或用户从未点过开关），
// 此时完全跟随环境变量，保证升级后行为不变。
func resolveAutoProcessMode(envMode string, enabled, configured bool) string {
	mode := normalizeAutoProcessMode(envMode)
	if mode == "" {
		// 环境变量层面就没开启：网页上的开关也无法打开，需要改容器配置。
		return ""
	}
	if configured && !enabled {
		return ""
	}
	return mode
}

// autoProcessEnabledOverride 读取设置页开关的显式取值。
//
// 第二个返回值表示“配置里是否写过这个键”：没有写过时沿用环境变量的默认行为，
// 这样老用户升级后不需要先去设置页点一下才会生效。
func autoProcessEnabledOverride(ctx context.Context) (bool, bool) {
	if !db.Initialized() {
		return false, false
	}
	cfg, err := db.Default().ListConfig(ctx)
	if err != nil {
		// 读配置失败时不做拦截，避免因为一次查询异常把自动整理静默停掉。
		logging.Error("auto process read config failed err=%v", err)
		return false, false
	}
	raw := strings.TrimSpace(cfg[autoProcessEnabledKey])
	if raw == "" {
		return false, false
	}
	return parseConfigBool(raw), true
}

// parseConfigBool 解析配置表里以字符串保存的布尔值。
func parseConfigBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// autoProcessJob 是一次待执行的自动整理请求。
type autoProcessJob struct {
	directory models.Directory
	mode      string
	// firstQueuedAt 是本轮入队周期的起点，等待封面的总时限以它为基准，
	// 这样每完成一轮整理重置 queuedAt 不会把等待时限一直往后推。
	firstQueuedAt time.Time
	// queuedAt 是最近一次进度变化的时刻，也用于快照排序。
	queuedAt time.Time
	// nextRoundAt 是封面仍在下载时下一轮整理的最早时刻。
	nextRoundAt time.Time
	// pending 是入队/上一轮结束时的未完成条目数，用于日志与进度判断。
	pending int
	// attempts 是本轮入队周期内已启动的整理次数。
	attempts int
	// running 表示上一轮整理已启动，等待它跑完再评估是否需要下一轮。
	running bool
	// loggedCovers 记录上次打印“等待封面”日志时的队列长度，避免刷屏。
	loggedCovers int
}

// autoProcessQueue 是“扫描完待整理”的目录集合，按目录 ID 去重。
//
// 用集合而不是 channel：同一目录被反复扫描只会刷新一条记录，
// 也不会因为缓冲写满而丢任务。
type autoProcessQueue struct {
	mu   sync.Mutex
	jobs map[int64]*autoProcessJob
}

func newAutoProcessQueue() *autoProcessQueue {
	return &autoProcessQueue{jobs: make(map[int64]*autoProcessJob)}
}

// put 登记一个目录，已在队列里时返回 false 且不改动已有记录：
// job 状态只在后台 worker 里修改，这样无需为字段加锁。
func (q *autoProcessQueue) put(job *autoProcessJob) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.jobs[job.directory.ID]; exists {
		return false
	}
	q.jobs[job.directory.ID] = job
	return true
}

func (q *autoProcessQueue) remove(id int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.jobs, id)
}

func (q *autoProcessQueue) snapshot() []*autoProcessJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	jobs := make([]*autoProcessJob, 0, len(q.jobs))
	for _, job := range q.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].queuedAt.Before(jobs[j].queuedAt) })
	return jobs
}

func (q *autoProcessQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

// clear 丢弃全部待整理任务，返回被丢弃的数量。
func (q *autoProcessQueue) clear() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	dropped := len(q.jobs)
	q.jobs = make(map[int64]*autoProcessJob)
	return dropped
}

// queueAutoProcess 在扫描结束后登记一次自动整理，立即返回。
//
// 这里刻意不做阻塞等待：扫描线程结束意味着界面上的“扫描中”要消失，
// 如果在这里等封面下载，用户看到的就是“扫描完了但什么都不发生”。
func (m *Manager) queueAutoProcess(directory models.Directory) {
	// 先做零成本判断，再碰数据库和磁盘：这些条件不满足时连开关都不用读。
	if directory.ID <= 0 || directory.IsDelete || directory.Missing {
		return
	}
	mode := m.autoProcessModeFor(m.ctx)
	if mode == "" {
		return
	}

	pending, err := countUnprocessedJavs(m.ctx, directory)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logging.Error("auto process probe failed id=%d path=%s err=%v", directory.ID, directory.Path, err)
		}
		return
	}
	if pending == 0 {
		return
	}

	job := &autoProcessJob{
		directory:     directory,
		mode:          mode,
		firstQueuedAt: time.Now(),
		queuedAt:      time.Now(),
		pending:       pending,
	}
	if !m.autoQueue.put(job) {
		logging.Info("auto process already queued id=%d path=%s pending=%d", directory.ID, directory.Path, pending)
		return
	}
	logging.Info(
		"auto process queued id=%d path=%s mode=%s pending=%d covers=%d",
		directory.ID,
		directory.Path,
		mode,
		pending,
		pendingCovers(),
	)
}

// pendingCovers 在封面队列未启用时返回 0，避免直接读取未初始化的队列。
func pendingCovers() int {
	if !cover.Enabled() {
		return 0
	}
	return cover.PendingCount()
}

// queueAutoProcessForAll 让所有可用目录重新评估一次是否需要自动整理。
//
// 用于设置页把开关从关闭改成开启的那一刻：否则用户要等到下一次扫描
// 才会看到补齐动作，容易误判成“开了也没用”。
func (m *Manager) queueAutoProcessForAll(ctx context.Context) {
	if m.autoProcessModeFor(ctx) == "" {
		return
	}
	directories, err := db.Default().ListActiveDirectories(ctx)
	if err != nil {
		logging.Error("auto process list directories failed err=%v", err)
		return
	}
	for _, directory := range directories {
		if ctx.Err() != nil {
			return
		}
		m.queueAutoProcess(directory)
	}
}

// StartAutoProcessScheduler 启动自动整理的后台 worker。
// 未配置模式时不启动，因此关闭自动整理时没有任何额外开销。
func (m *Manager) StartAutoProcessScheduler(ctx context.Context, pollInterval time.Duration) {
	if normalizeAutoProcessMode(m.autoProcessMode) == "" {
		return
	}
	if pollInterval <= 0 {
		pollInterval = defaultAutoProcessCoverPoll
	}
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			m.runAutoProcessCycle(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// runAutoProcessCycle 检查一次队列：能整理就整理，需要等封面就继续等。
func (m *Manager) runAutoProcessCycle(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// 队列为空时直接返回，不查询配置表：这个函数每 30 秒跑一次，
	// 空闲状态下不应该产生任何数据库开销。
	if m.autoQueue.len() == 0 {
		return
	}
	// 开关可能刚在设置页被关掉：丢掉已排队的任务，避免关掉之后还在后台写文件。
	// 已经开始的那一轮整理无法中途取消，会自然跑完。
	if m.autoProcessModeFor(ctx) == "" {
		if dropped := m.autoQueue.clear(); dropped > 0 {
			logging.Info("auto process disabled, dropped %d queued job(s)", dropped)
		}
		return
	}
	jobs := m.autoQueue.snapshot()
	if len(jobs) == 0 {
		return
	}

	covers := pendingCovers()
	wait := m.autoProcessCoverWait
	switch {
	case wait < 0:
		// 负值表示完全不等待封面。
		wait = 0
	case wait == 0:
		wait = defaultAutoProcessCoverWait
	}

	for _, job := range jobs {
		id := job.directory.ID
		// 目录正被扫描或整理时不要插队：StartDirectoryProcessing 会取消进行中的扫描，
		// 让用户点了「扫描」又被自动流程打断是不可接受的。
		if m.IsDirectoryScanning(id) || m.activeDirectoryProcessingStatus(id) != "" {
			continue
		}
		if job.running {
			m.settleAutoProcessJob(ctx, job)
			continue
		}
		// 首轮：等封面队列排空，最多 wait 这么长时间。
		if job.attempts == 0 && covers > 0 && wait > 0 {
			waited := time.Since(job.firstQueuedAt)
			if waited < wait {
				// 只在队列长度变化时打日志，否则等待期间会刷出上百行。
				if covers != job.loggedCovers {
					job.loggedCovers = covers
					logging.Info(
						"auto process waiting for covers id=%d pending=%d covers=%d waited=%s",
						id,
						job.pending,
						covers,
						waited.Round(time.Second),
					)
				}
				continue
			}
			logging.Error(
				"auto process cover wait timed out id=%d covers=%d waited=%s; proceeding with cached covers",
				id,
				covers,
				waited.Round(time.Second),
			)
		}
		// 后续轮：封面还在下载时按固定间隔重试，避免每 30 秒全量重扫磁盘。
		if job.attempts > 0 && covers > 0 && time.Now().Before(job.nextRoundAt) {
			continue
		}
		m.startAutoProcessJob(ctx, job)
	}
}

// notifyEmbyAfterAutoProcess 在自动整理完成一个目录后通知 Emby 刷新。
// 未配置 Emby 联动时静默跳过；失败只记日志。受 emby.NotifyRefresh 的
// 最小间隔限制，连续完成多个目录也只会定期刷一次。
func notifyEmbyAfterAutoProcess(ctx context.Context) {
	if !db.Initialized() {
		return
	}
	cfg, err := db.Default().ListConfig(ctx)
	if err != nil {
		return
	}
	emby.NotifyRefresh(ctx, cfg, false)
}

// settleAutoProcessJob 在上一轮整理结束后重新统计进度，决定收尾还是再来一轮。
func (m *Manager) settleAutoProcessJob(ctx context.Context, job *autoProcessJob) {
	pending, err := countUnprocessedJavs(ctx, job.directory)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logging.Error("auto process recheck failed id=%d path=%s err=%v", job.directory.ID, job.directory.Path, err)
		}
		m.autoQueue.remove(job.directory.ID)
		return
	}
	if pending == 0 {
		logging.Info(
			"auto process finished id=%d path=%s rounds=%d",
			job.directory.ID,
			job.directory.Path,
			job.attempts,
		)
		m.autoQueue.remove(job.directory.ID)
		// 目录整理完成：通知 Emby 刷新（受最小间隔限制，高频完成不会连续打扰）。
		notifyEmbyAfterAutoProcess(ctx)
		return
	}

	// 还有进展就继续下一轮：封面是分批下载的，每轮都能多写出一批。
	if pending < job.pending {
		logging.Info(
			"auto process progress id=%d path=%s remaining=%d rounds=%d",
			job.directory.ID,
			job.directory.Path,
			pending,
			job.attempts,
		)
		job.pending = pending
		job.running = false
		job.queuedAt = time.Now()
		job.nextRoundAt = time.Now().Add(autoProcessRoundInterval)
		return
	}

	// 没有进展：封面还在下载就值得再等，否则说明这些条目本身写不进去。
	if pendingCovers() > 0 && job.attempts < autoProcessMaxAttempts {
		job.running = false
		job.queuedAt = time.Now()
		job.nextRoundAt = time.Now().Add(autoProcessRoundInterval)
		return
	}
	logging.Error(
		"auto process stalled id=%d path=%s remaining=%d rounds=%d; will retry on next scan",
		job.directory.ID,
		job.directory.Path,
		pending,
		job.attempts,
	)
	m.autoQueue.remove(job.directory.ID)
}

// startAutoProcessJob 启动一轮整理，统一用 skipCoverWait 模式避免逐条等封面。
func (m *Manager) startAutoProcessJob(ctx context.Context, job *autoProcessJob) {
	id := job.directory.ID
	if job.attempts >= autoProcessMaxAttempts {
		logging.Error(
			"auto process attempts exhausted id=%d path=%s rounds=%d remaining=%d; will retry on next scan",
			id,
			job.directory.Path,
			job.attempts,
			job.pending,
		)
		m.autoQueue.remove(id)
		return
	}

	err := m.startDirectoryProcessing(ctx, job.directory, job.mode, DirectoryProcessLayoutPrefix, true)
	if err != nil {
		switch {
		case errors.Is(err, ErrDirectoryWorkInProgress), errors.Is(err, ErrDirectoryScanInProgress):
			// 用户手动整理或扫描正在进行，等它结束后再评估。
			logging.Info("auto process skipped, directory busy id=%d", id)
			job.running = true
			job.attempts++
		case errors.Is(err, context.Canceled):
			return
		default:
			logging.Error("auto process start failed id=%d path=%s err=%v", id, job.directory.Path, err)
			m.autoQueue.remove(id)
		}
		return
	}

	job.running = true
	job.attempts++
	logging.Info(
		"auto process started id=%d path=%s mode=%s round=%d remaining=%d",
		id,
		job.directory.Path,
		job.mode,
		job.attempts,
		job.pending,
	)
}

// countUnprocessedJavs 统计目录下“已关联 JAV、但 NFO 或封面还没写好”的视频数量。
//
// 只有 NFO 和海报都落盘才算完成：writeJavSidecars 先写 NFO 再写封面，
// 封面没下载好时只记 posterPending，因此单看 NFO 会把缺图条目误判成已完成。
// 未启用封面下载（没有 CoverDir）时放弃海报判定，否则永远清不了队列。
func countUnprocessedJavs(ctx context.Context, directory models.Directory) (int, error) {
	root := strings.TrimSpace(directory.Path)
	if root == "" || !filepath.IsAbs(root) {
		return 0, errors.New("directory path is not absolute")
	}
	candidates, err := db.Default().ListJavProcessingCandidates(ctx, directory.ID)
	if err != nil {
		return 0, err
	}
	requirePoster := cover.Enabled()
	pending := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		relative := strings.TrimSpace(candidate.RelativePath)
		if relative == "" {
			continue
		}
		videoPath := filepath.Join(root, filepath.FromSlash(relative))
		if !javSidecarComplete(videoPath, requirePoster) {
			pending++
		}
	}
	return pending, nil
}

// javSidecarImageExts 是海报可能落盘的扩展名，取决于刮削源给的封面格式。
var javSidecarImageExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// javSidecarComplete 报告视频旁的 NFO 与海报是否都已由 JavBoss 写好。
//
// NFO 判定沿用 canWriteJavBossNFO 的三种结果：文件不存在与第三方 NFO
// 都算未完成——第三方 NFO 的条目仍需整理（保留它的 NFO，补写封面）。
func javSidecarComplete(videoPath string, requirePoster bool) bool {
	base := strings.TrimSuffix(videoPath, filepath.Ext(videoPath))
	owned, err := canWriteJavBossNFO(base + ".nfo")
	if err != nil || !owned {
		return false
	}
	if !requirePoster {
		return true
	}
	return javPosterExists(base)
}

// javPosterExists 判断视频旁是否已有非空的海报文件。
func javPosterExists(base string) bool {
	for _, ext := range javSidecarImageExts {
		info, err := os.Stat(base + posterSuffix + ext)
		if err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
}
