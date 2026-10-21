package directory

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"javboss/internal/common/logging"
	"javboss/internal/db"
	"javboss/internal/emby"
	"javboss/internal/models"
)

// NFO 重建：把"空壳"NFO 用数据库里的刮削结果重写一遍。
//
// 背景：JavBoss 生成的 NFO 一定带 <generator>JavBoss</generator>。
// 媒体目录里经常还留着别的刮削器（Emby 插件、MetaTube、tinyMediaManager）写过的 NFO，
// 其中一部分是"空壳"——只有番号和 runtime，plot / genre / actor 全是空的。
// 这类文件不含 JavBoss 标记，自动整理会按"别人的文件"保护起来不覆盖，
// 于是空壳一直存在。本文件提供的是一次性的批量重建，只覆盖：
//   * JavBoss 自己写的（刷新成最新数据）
//   * 空壳（没有正文内容的外部 NFO）
//   * 缺失的
// 已经有剧情简介等正文的外部 NFO 一律不碰。

// ErrNFODirectoryNotFound 表示要重建的目录不存在或已被删除。
var ErrNFODirectoryNotFound = errors.New("directory not found for NFO rebuild")

// nfoRebuildProgressInterval 控制进度回传频率（按视频条数），
// 避免每写一个文件都去抢一次锁。
const nfoRebuildProgressInterval = 50

// NFOInspectState 描述一个 .nfo 文件的现状。
type NFOInspectState int

const (
	// NFOMissing 表示视频旁边没有 NFO。
	NFOMissing NFOInspectState = iota
	// NFOOwnedByJavBoss 表示 NFO 由 JavBoss 生成，可以随意刷新。
	NFOOwnedByJavBoss
	// NFOShell 表示外部工具写的空壳 NFO：有文件但没有正文内容。
	NFOShell
	// NFORich 表示外部工具写的、带正文内容的 NFO，必须保留。
	NFORich
)

// nfoProbe 只取判断"是否空壳"需要的字段。
type nfoProbe struct {
	Generator string   `xml:"generator"`
	Plot      string   `xml:"plot"`
	Summary   string   `xml:"summary"`
	Outline   string   `xml:"outline"`
	Genres    []string `xml:"genre"`
	Actors    []string `xml:"actor>name"`
}

// NFORebuildSummary 是一次批量重建的统计结果。
type NFORebuildSummary struct {
	DryRun      bool     `json:"dry_run"`
	Directories int      `json:"directories"`
	Videos      int      `json:"videos"`
	Missing     int      `json:"missing"`
	Shells      int      `json:"shells"`
	Owned       int      `json:"owned"`
	Preserved   int      `json:"preserved"`
	Rebuilt     int      `json:"rebuilt"`
	NoMetadata  int      `json:"no_metadata"`
	Failed      int      `json:"failed"`
	Samples     []string `json:"samples,omitempty"`
	Errors      []string `json:"errors,omitempty"`
}

// NFORebuildStatus 是前端轮询用的任务状态快照。
type NFORebuildStatus struct {
	Running    bool              `json:"running"`
	DryRun     bool              `json:"dry_run"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at"`
	Summary    NFORebuildSummary `json:"summary"`
	Error      string            `json:"error,omitempty"`
}

var (
	nfoRebuildMu      sync.Mutex
	nfoRebuildRunning bool
	nfoRebuildState   NFORebuildStatus
)

func (s *NFORebuildSummary) addError(message string) {
	if len(s.Errors) >= 20 {
		return
	}
	s.Errors = append(s.Errors, message)
}

func (s *NFORebuildSummary) addSample(code string) {
	if len(s.Samples) >= 15 || code == "" {
		return
	}
	s.Samples = append(s.Samples, code)
}

// merge 合并另一个目录的统计。
func (s *NFORebuildSummary) merge(other NFORebuildSummary) {
	s.Directories += other.Directories
	s.Videos += other.Videos
	s.Missing += other.Missing
	s.Shells += other.Shells
	s.Owned += other.Owned
	s.Preserved += other.Preserved
	s.Rebuilt += other.Rebuilt
	s.NoMetadata += other.NoMetadata
	s.Failed += other.Failed
	for _, sample := range other.Samples {
		s.addSample(sample)
	}
	for _, message := range other.Errors {
		s.addError(message)
	}
}

// inspectNFO 读取并分类一个 NFO 文件。
//
// 解析失败时返回 NFORich：读不懂的文件一律不覆盖，宁可漏掉也不误删别人的数据。
func inspectNFO(path string) (NFOInspectState, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NFOMissing, nil
	}
	if err != nil {
		return NFOMissing, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return NFOShell, nil
	}
	if bytes.Contains(trimmed, []byte("<generator>JavBoss</generator>")) {
		return NFOOwnedByJavBoss, nil
	}
	var probe nfoProbe
	if err := xml.Unmarshal(trimmed, &probe); err != nil {
		return NFORich, nil
	}
	if strings.TrimSpace(probe.Plot) != "" ||
		strings.TrimSpace(probe.Summary) != "" ||
		strings.TrimSpace(probe.Outline) != "" ||
		len(probe.Genres) > 0 ||
		len(probe.Actors) > 0 {
		return NFORich, nil
	}
	return NFOShell, nil
}

// javHasMetadata 判断这条记录重写后能不能写出比空壳更有用的 NFO。
// 数据全空的记录（还没刮削成功）不重建，否则只是把空壳换个样子。
func javHasMetadata(item *models.Jav) bool {
	if item == nil {
		return false
	}
	if strings.TrimSpace(item.Title) != "" || strings.TrimSpace(item.ZhTitle) != "" {
		return true
	}
	if item.Studio != nil && strings.TrimSpace(item.Studio.Name) != "" {
		return true
	}
	if item.Series != nil && strings.TrimSpace(item.Series.Name) != "" {
		return true
	}
	// 只有番号和时长不算"有元数据"：时长来自视频文件本身，
	// 写出来仍然是空壳，这类条目应该先去补刮削。
	if item.ReleaseUnix > 0 {
		return true
	}
	return len(item.Tags) > 0 || len(item.Idols) > 0
}

// StartNFORebuild 在后台启动一次重建；同一时间只允许一个任务。
// directoryID <= 0 表示所有可用目录；dryRun 只统计不落盘。
func StartNFORebuild(directoryID int64, dryRun bool) bool {
	nfoRebuildMu.Lock()
	if nfoRebuildRunning {
		nfoRebuildMu.Unlock()
		return false
	}
	nfoRebuildRunning = true
	nfoRebuildState = NFORebuildStatus{
		Running:   true,
		DryRun:    dryRun,
		StartedAt: time.Now(),
	}
	nfoRebuildMu.Unlock()

	go func() {
		ctx := context.Background()
		summary, err := rebuildNFOs(ctx, directoryID, dryRun, func(update NFORebuildSummary) {
			nfoRebuildMu.Lock()
			nfoRebuildState.Summary = update
			nfoRebuildMu.Unlock()
		})

		nfoRebuildMu.Lock()
		nfoRebuildState.Running = false
		nfoRebuildState.DryRun = dryRun
		nfoRebuildState.Summary = summary
		nfoRebuildState.FinishedAt = time.Now()
		if err != nil {
			nfoRebuildState.Error = err.Error()
		}
		nfoRebuildRunning = false
		nfoRebuildMu.Unlock()

		logging.Info(
			"nfo rebuild finished directory_id=%d dry_run=%t videos=%d shells=%d missing=%d rebuilt=%d preserved=%d failed=%d err=%v",
			directoryID, dryRun, summary.Videos, summary.Shells, summary.Missing, summary.Rebuilt, summary.Preserved, summary.Failed, err,
		)

		// Emby 不会因为磁盘上的 NFO 变了就自动重读，重建完必须通知它刷新一次，
		// 否则用户在 Emby 里看到的还是旧元数据。dry run 没有写文件，不需要刷；
		// 通知失败只记日志，不影响重建本身的结果。
		if !dryRun {
			if cfg, cfgErr := db.Default().ListConfig(ctx); cfgErr == nil {
				emby.NotifyRefresh(ctx, cfg, true)
			}
		}
	}()
	return true
}

// CurrentNFORebuildStatus 返回任务状态快照，供前端轮询。
func CurrentNFORebuildStatus() NFORebuildStatus {
	nfoRebuildMu.Lock()
	defer nfoRebuildMu.Unlock()
	return nfoRebuildState
}

// rebuildNFOs 同步执行重建，progress 用于回传进度（可为 nil）。
func rebuildNFOs(
	ctx context.Context,
	directoryID int64,
	dryRun bool,
	progress func(NFORebuildSummary),
) (NFORebuildSummary, error) {
	total := NFORebuildSummary{DryRun: dryRun}

	directories := []models.Directory{}
	if directoryID > 0 {
		dir, err := db.Default().GetDirectory(ctx, directoryID)
		if err != nil {
			return total, err
		}
		if dir == nil || dir.IsDelete {
			return total, ErrNFODirectoryNotFound
		}
		directories = append(directories, *dir)
	} else {
		list, err := db.Default().ListActiveDirectories(ctx)
		if err != nil {
			return total, err
		}
		directories = list
	}

	for _, dir := range directories {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if dir.Missing {
			logging.Info("nfo rebuild skipped missing directory id=%d path=%s", dir.ID, dir.Path)
			continue
		}
		part := rebuildNFOsForDirectory(ctx, dir, dryRun, func(partial NFORebuildSummary) {
			if progress == nil {
				return
			}
			snapshot := total
			snapshot.merge(partial)
			progress(snapshot)
		})
		total.merge(part)
		if part.Rebuilt > 0 || part.Shells > 0 {
			logging.Info(
				"nfo rebuild directory id=%d path=%s videos=%d shells=%d rebuilt=%d preserved=%d dry_run=%t",
				dir.ID, dir.Path, part.Videos, part.Shells, part.Rebuilt, part.Preserved, dryRun,
			)
		}
		if progress != nil {
			progress(total)
		}
	}
	return total, ctx.Err()
}

// rebuildNFOsForDirectory 处理单个目录。
func rebuildNFOsForDirectory(
	ctx context.Context,
	dir models.Directory,
	dryRun bool,
	onProgress func(NFORebuildSummary),
) NFORebuildSummary {
	summary := NFORebuildSummary{DryRun: dryRun, Directories: 1}
	if strings.TrimSpace(dir.Path) == "" {
		summary.addError("目录路径为空")
		return summary
	}

	items, err := db.Default().ListJavsForDirectoryProcessing(ctx, dir.ID)
	if err != nil {
		summary.addError(fmt.Sprintf("读取目录 %d 的媒体记录失败：%v", dir.ID, err))
		return summary
	}

	for i := range items {
		if ctx.Err() != nil {
			return summary
		}
		rebuildNFOsForItem(ctx, dir.Path, &items[i], dryRun, &summary)
		if onProgress != nil && summary.Videos > 0 && summary.Videos%nfoRebuildProgressInterval == 0 {
			onProgress(summary)
		}
	}
	return summary
}

// rebuildNFOsForItem 处理一条 JAV 记录关联的所有视频文件。
func rebuildNFOsForItem(
	ctx context.Context,
	root string,
	item *models.Jav,
	dryRun bool,
	summary *NFORebuildSummary,
) {
	if item == nil || summary == nil {
		return
	}
	code := strings.ToUpper(strings.TrimSpace(item.Code))
	ready := javHasMetadata(item)

	for i := range item.Videos {
		if ctx.Err() != nil {
			return
		}
		absolute, err := safeDirectoryFilePath(root, item.Videos[i].Path)
		if err != nil {
			summary.Failed++
			summary.addError(fmt.Sprintf("%s: %v", item.Videos[i].Path, err))
			continue
		}
		nfoPath := strings.TrimSuffix(absolute, filepath.Ext(absolute)) + ".nfo"
		state, err := inspectNFO(nfoPath)
		if err != nil {
			summary.Failed++
			summary.addError(fmt.Sprintf("%s: %v", reportRelativePath(root, nfoPath), err))
			continue
		}

		summary.Videos++
		switch state {
		case NFOMissing:
			summary.Missing++
		case NFOShell:
			summary.Shells++
		case NFOOwnedByJavBoss:
			summary.Owned++
		default:
			// 外部工具写的完整 NFO：保留，不参与重建。
			summary.Preserved++
			continue
		}

		if !ready {
			// 数据库里没有可用元数据，重写只会再次得到空壳。
			summary.NoMetadata++
			continue
		}
		if dryRun {
			continue
		}
		if err := writeNFOFile(nfoPath, makeJellyfinNFO(item)); err != nil {
			summary.Failed++
			summary.addError(fmt.Sprintf("%s: %v", reportRelativePath(root, nfoPath), err))
			continue
		}
		summary.Rebuilt++
		summary.addSample(code)
	}
}
