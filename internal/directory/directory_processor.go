package directory

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"javboss/internal/common/logging"
	"javboss/internal/db"
	"javboss/internal/jav/scrape/cover"
	"javboss/internal/models"
	"javboss/internal/util"
)

const (
	DirectoryProcessSidecar             = "sidecar"
	DirectoryProcessOrganize            = "organize"
	DirectoryProcessOrganizeWithSidecar = "organize_with_sidecar"
	DirectoryProcessLayoutPrefix        = "prefix"
	DirectoryProcessLayoutCode          = "code"
	DirectoryProcessLayoutIdol          = "idol"
	directoryOrganizeParent             = "JAV"
	directoryUnknownIdol                = "未知女优"
	directoryMultipleIdols              = "多女优"
	directoryMaxJoinedIdols             = 3
	directoryProcessReportName          = "JavBoss-整理报告.txt"
	// coverEnsureTimeout bounds the synchronous cover fetch performed while
	// writing a poster, so a slow provider cannot stall the whole job.
	coverEnsureTimeout = 30 * time.Second
	// defaultPosterCropLeft 是 DVD 封套左侧小图占整幅宽度的典型比例。
	defaultPosterCropLeft = 0.53
	// maxPosterCropLeft 防止误配置把整张封面裁没。
	maxPosterCropLeft = 0.95
	// posterSuffix / fanartSuffix 是 Emby 认可的图片后缀写法。
	// {name}-poster.ext 在 Emby 的 Primary 图片优先序列中排在裸名 poster.ext 之前，
	// 且不要求“一片一目录”，因此 sidecar 图片统一带视频名。
	posterSuffix = "-poster"
	fanartSuffix = "-fanart"
)

var (
	ErrInvalidDirectoryProcessMode   = errors.New("invalid directory process mode")
	ErrInvalidDirectoryProcessLayout = errors.New("invalid directory process layout")
	ErrDirectoryWorkInProgress       = errors.New("directory work in progress")
)

// DirectoryProcessSummary records the result of one directory processing job.
type DirectoryProcessSummary struct {
	Locations        int
	Moved            int
	AlreadyOrganized int
	Sidecars         int
	// NFOPreserved counts entries whose existing NFO belongs to another tool
	// and was left untouched while the cover was still processed.
	NFOPreserved int
	// PostersWritten counts poster images written next to the media file.
	PostersWritten int
	// FanartsWritten counts fanart images written next to the media file.
	FanartsWritten int
	// PostersPending counts entries that could not get a cover in this run.
	PostersPending           int
	Skipped                  int
	Failed                   int
	EmptyDirectoriesRemoved  int
	MoveFailures             []DirectoryProcessIssue
	SkippedItems             []DirectoryProcessIssue
	SidecarFailures          []DirectoryProcessIssue
	DirectoryCleanupFailures []DirectoryProcessIssue
	emptyDirectoryCandidates []string
}

// errNFOUnownedByOtherTool marks a pre-existing NFO that JavBoss must not overwrite.
var errNFOUnownedByOtherTool = errors.New("existing NFO is not managed by JavBoss")

// DirectoryProcessIssue describes one file that was not fully processed.
type DirectoryProcessIssue struct {
	Code       string
	SourcePath string
	TargetPath string
	Reason     string
}

func directoryProcessMode(raw string) (mode string, status string, ok bool) {
	mode = strings.TrimSpace(raw)
	switch mode {
	case DirectoryProcessSidecar:
		return mode, DirectoryWorkGeneratingSidecar, true
	case DirectoryProcessOrganize:
		return mode, DirectoryWorkOrganizing, true
	case DirectoryProcessOrganizeWithSidecar:
		return mode, DirectoryWorkOrganizingWithSidecar, true
	default:
		return "", "", false
	}
}

func (m *Manager) activeDirectoryProcessingStatus(id int64) string {
	m.directoryProcessingMu.Lock()
	defer m.directoryProcessingMu.Unlock()
	return m.directoryProcessingStatus[id]
}

func (m *Manager) setDirectoryProcessingStatus(id int64, status string) {
	m.directoryProcessingMu.Lock()
	defer m.directoryProcessingMu.Unlock()
	if status == "" {
		delete(m.directoryProcessingStatus, id)
		return
	}
	m.directoryProcessingStatus[id] = status
}

// StartDirectoryProcessing reserves the directory and starts an asynchronous filesystem job.
func (m *Manager) StartDirectoryProcessing(ctx context.Context, directory models.Directory, mode, layout string) error {
	return m.startDirectoryProcessing(ctx, directory, mode, layout, false)
}

// startDirectoryProcessing 是 StartDirectoryProcessing 的内部实现。
//
// noCoverWait 为 true 时本次整理只使用本地已有封面，缺失的条目记
// posterPending 并交给后台队列继续下载，由后续轮次补齐。自动整理走这条路，
// 否则整库首次入库会变成“每条缺图各等 30 秒”的长尾。
func (m *Manager) startDirectoryProcessing(
	ctx context.Context,
	directory models.Directory,
	mode, layout string,
	noCoverWait bool,
) error {
	if err := m.ctx.Err(); err != nil {
		return err
	}
	normalizedMode, status, ok := directoryProcessMode(mode)
	if !ok {
		return ErrInvalidDirectoryProcessMode
	}
	mode = normalizedMode
	layout, ok = directoryProcessLayout(layout)
	if !ok {
		return ErrInvalidDirectoryProcessLayout
	}
	if directory.ID <= 0 || directory.IsDelete {
		return errors.New("invalid directory")
	}
	m.directoryProcessingMu.Lock()
	if m.directoryProcessingStatus[directory.ID] != "" {
		m.directoryProcessingMu.Unlock()
		return ErrDirectoryWorkInProgress
	}
	m.directoryProcessingStatus[directory.ID] = status
	m.directoryProcessingMu.Unlock()
	release, err := m.CancelAndReserveDirectoryScan(ctx, directory.ID)
	if err != nil {
		m.setDirectoryProcessingStatus(directory.ID, "")
		return err
	}

	processCtx := m.ctx
	if noCoverWait {
		processCtx = withSkipCoverWait(processCtx)
	}

	go func() {
		defer m.setDirectoryProcessingStatus(directory.ID, "")
		defer release()

		startedAt := time.Now()
		summary, processErr := ProcessDirectory(processCtx, m.coverDir, directory, mode, layout)
		finishedAt := time.Now()
		if processErr != nil {
			logging.Error("directory processing failed id=%d mode=%s err=%v", directory.ID, mode, processErr)
		} else {
			logging.Info(
				"directory processing complete id=%d mode=%s locations=%d moved=%d already_organized=%d sidecars=%d nfo_preserved=%d posters_written=%d fanarts_written=%d posters_pending=%d skipped=%d failed=%d empty_directories_removed=%d cleanup_failed=%d",
				directory.ID,
				mode,
				summary.Locations,
				summary.Moved,
				summary.AlreadyOrganized,
				summary.Sidecars,
				summary.NFOPreserved,
				summary.PostersWritten,
				summary.FanartsWritten,
				summary.PostersPending,
				summary.Skipped,
				summary.Failed,
				summary.EmptyDirectoriesRemoved,
				len(summary.DirectoryCleanupFailures),
			)
		}
		if err := writeDirectoryProcessReport(
			directory.Path,
			mode,
			layout,
			summary,
			processErr,
			startedAt,
			finishedAt,
		); err != nil {
			logging.Error("write directory processing report failed id=%d path=%s err=%v", directory.ID, directory.Path, err)
		}

		release()
		if m.ctx.Err() != nil {
			return
		}
		m.setDirectoryProcessingStatus(directory.ID, DirectoryWorkScanning)
		if _, err := m.ScanDirectory(m.ctx, directory); err != nil &&
			!errors.Is(err, ErrDirectoryScanInProgress) {
			logging.Error("rescan after directory processing failed id=%d err=%v", directory.ID, err)
		}
	}()
	return nil
}

func directoryProcessLayout(layout string) (string, bool) {
	switch strings.TrimSpace(layout) {
	case "", DirectoryProcessLayoutPrefix:
		return DirectoryProcessLayoutPrefix, true
	case DirectoryProcessLayoutCode:
		return DirectoryProcessLayoutCode, true
	case DirectoryProcessLayoutIdol:
		return DirectoryProcessLayoutIdol, true
	default:
		return "", false
	}
}

// ProcessDirectory organizes and/or writes Sidecar files for scraped locations.
func ProcessDirectory(
	ctx context.Context,
	coverDir string,
	directory models.Directory,
	mode string,
	layout string,
) (*DirectoryProcessSummary, error) {
	normalizedMode, _, ok := directoryProcessMode(mode)
	if !ok {
		return nil, ErrInvalidDirectoryProcessMode
	}
	mode = normalizedMode
	layout, ok = directoryProcessLayout(layout)
	if !ok {
		return nil, ErrInvalidDirectoryProcessLayout
	}
	if strings.TrimSpace(directory.Path) == "" {
		return nil, errors.New("directory path is empty")
	}

	items, err := db.Default().ListJavsForDirectoryProcessing(ctx, directory.ID)
	if err != nil {
		return nil, err
	}

	summary := &DirectoryProcessSummary{}
	for i := range items {
		if err := ctx.Err(); err != nil {
			cleanupEmptySourceDirectories(directory.Path, summary)
			return summary, err
		}
		processJavItem(ctx, directory.Path, &items[i], mode, layout, coverDir, summary)
	}
	cleanupEmptySourceDirectories(directory.Path, summary)
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	return summary, nil
}

func processJavItem(
	ctx context.Context,
	root string,
	item *models.Jav,
	mode string,
	layout string,
	coverDir string,
	summary *DirectoryProcessSummary,
) {
	if item == nil || summary == nil {
		return
	}
	summary.Locations += len(item.Videos)
	code, prefix, ok := organizeCodeParts(item.Code)
	if !ok {
		summary.Skipped += len(item.Videos)
		for i := range item.Videos {
			summary.SkippedItems = append(summary.SkippedItems, DirectoryProcessIssue{
				Code:       strings.TrimSpace(item.Code),
				SourcePath: reportRelativePath(root, item.Videos[i].Path),
				Reason:     "番号为空或无法生成安全的目录名",
			})
		}
		return
	}

	for i := range item.Videos {
		if err := ctx.Err(); err != nil {
			return
		}
		video := &item.Videos[i]
		source, err := safeDirectoryFilePath(root, video.Path)
		if err != nil {
			summary.Failed++
			summary.MoveFailures = append(summary.MoveFailures, DirectoryProcessIssue{
				Code:       code,
				SourcePath: reportRelativePath(root, video.Path),
				Reason:     directoryProcessFailureReason(err),
			})
			logging.Error("resolve directory processing source failed path=%s err=%v", video.Path, err)
			continue
		}
		target := source
		if mode != DirectoryProcessSidecar {
			group := prefix
			if layout == DirectoryProcessLayoutCode {
				target, err = safeDirectoryFilePath(
					root,
					filepath.Join(directoryOrganizeParent, code, filepath.Base(source)),
				)
			} else {
				if layout == DirectoryProcessLayoutIdol {
					group = organizeIdolComponent(item.Idols)
				}
				target, err = safeDirectoryFilePath(
					root,
					filepath.Join(directoryOrganizeParent, group, code, filepath.Base(source)),
				)
			}
			if err != nil {
				summary.Failed++
				summary.MoveFailures = append(summary.MoveFailures, DirectoryProcessIssue{
					Code:       code,
					SourcePath: reportRelativePath(root, source),
					Reason:     directoryProcessFailureReason(err),
				})
				logging.Error("resolve directory processing target failed path=%s err=%v", video.Path, err)
				continue
			}
			moved, moveErr := moveMediaGroup(source, target)
			if moveErr != nil {
				summary.Failed++
				summary.MoveFailures = append(summary.MoveFailures, DirectoryProcessIssue{
					Code:       code,
					SourcePath: reportRelativePath(root, source),
					TargetPath: reportRelativePath(root, target),
					Reason:     directoryProcessFailureReason(moveErr),
				})
				logging.Error("move directory media failed source=%s target=%s err=%v", source, target, moveErr)
				continue
			}
			if moved {
				summary.Moved++
				summary.emptyDirectoryCandidates = append(
					summary.emptyDirectoryCandidates,
					filepath.Dir(source),
				)
			} else {
				summary.AlreadyOrganized++
			}
		}

		if mode == DirectoryProcessSidecar || mode == DirectoryProcessOrganizeWithSidecar {
			result, err := writeJavSidecars(ctx, target, item, coverDir)
			if result.changed() {
				summary.Sidecars++
			}
			if result.nfoPreserved {
				summary.NFOPreserved++
			}
			if result.posterWritten {
				summary.PostersWritten++
			}
			if result.fanartWritten {
				summary.FanartsWritten++
			}
			if result.posterPending {
				summary.PostersPending++
			}
			if err != nil {
				summary.Failed++
				summary.SidecarFailures = append(summary.SidecarFailures, DirectoryProcessIssue{
					Code:       code,
					SourcePath: reportRelativePath(root, target),
					Reason:     directoryProcessFailureReason(err),
				})
				logging.Error("write JAV Sidecar failed path=%s code=%s err=%v", target, item.Code, err)
				continue
			}
		}
	}
}

func organizeIdolComponent(idols []models.JavIdol) string {
	names := make([]string, 0, len(idols))
	seen := make(map[string]struct{}, len(idols))
	for i := range idols {
		name := firstNonEmptyString(
			idols[i].ChineseName,
			idols[i].Name,
			idols[i].JapaneseName,
			idols[i].RomanName,
		)
		name = sanitizeOrganizeComponent(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return directoryUnknownIdol
	}
	if len(names) > directoryMaxJoinedIdols {
		return directoryMultipleIdols
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return strings.Join(names, "，")
}

func organizeCodeParts(raw string) (code string, prefix string, ok bool) {
	code = sanitizeOrganizeComponent(strings.ToUpper(strings.TrimSpace(raw)))
	if code == "" {
		return "", "", false
	}
	if index := strings.IndexAny(code, "-_"); index > 0 {
		prefix = code[:index]
	} else {
		prefix = "OTHER"
	}
	prefix = sanitizeOrganizeComponent(prefix)
	return code, prefix, prefix != ""
}

func sanitizeOrganizeComponent(value string) string {
	var builder strings.Builder
	lastSeparator := false
	for _, r := range value {
		allowed := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.'
		if allowed {
			builder.WriteRune(r)
			lastSeparator = false
			continue
		}
		if !lastSeparator {
			builder.WriteByte('_')
			lastSeparator = true
		}
	}
	return strings.Trim(builder.String(), " ._-")
}

func reportRelativePath(root, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "（未知）"
	}
	cleaned := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(cleaned) {
		if relative, err := filepath.Rel(filepath.Clean(root), cleaned); err == nil &&
			relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			cleaned = relative
		}
	}
	return filepath.ToSlash(cleaned)
}

func pathWithinDirectory(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func addDirectoryCleanupFailure(
	root string,
	path string,
	reason string,
	summary *DirectoryProcessSummary,
) {
	if summary == nil {
		return
	}
	summary.DirectoryCleanupFailures = append(
		summary.DirectoryCleanupFailures,
		DirectoryProcessIssue{
			SourcePath: reportRelativePath(root, path),
			Reason:     reason,
		},
	)
}

func cleanupEmptySourceDirectories(root string, summary *DirectoryProcessSummary) {
	if summary == nil || len(summary.emptyDirectoryCandidates) == 0 {
		return
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		addDirectoryCleanupFailure(root, root, "所选目录不是有效的绝对路径", summary)
		return
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		addDirectoryCleanupFailure(root, root, directoryProcessFailureReason(err), summary)
		return
	}

	candidates := make(map[string]struct{})
	for _, candidate := range summary.emptyDirectoryCandidates {
		current := filepath.Clean(candidate)
		for current != root && pathWithinDirectory(root, current) {
			candidates[current] = struct{}{}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		return len(paths[i]) > len(paths[j])
	})

	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			addDirectoryCleanupFailure(root, path, directoryProcessFailureReason(err), summary)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			addDirectoryCleanupFailure(root, path, "为避免越过符号链接，未自动删除此目录", summary)
			continue
		}
		if !info.IsDir() {
			continue
		}

		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			addDirectoryCleanupFailure(root, path, directoryProcessFailureReason(err), summary)
			continue
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			addDirectoryCleanupFailure(root, path, directoryProcessFailureReason(err), summary)
			continue
		}
		expectedRealPath := filepath.Join(realRoot, relativePath)
		if !pathWithinDirectory(realRoot, realPath) ||
			filepath.Clean(realPath) != filepath.Clean(expectedRealPath) {
			addDirectoryCleanupFailure(root, path, "为避免越过符号链接，未自动删除此目录", summary)
			continue
		}

		entries, err := os.ReadDir(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			addDirectoryCleanupFailure(root, path, directoryProcessFailureReason(err), summary)
			continue
		}
		if len(entries) != 0 {
			continue
		}
		if err := os.Remove(path); err != nil {
			addDirectoryCleanupFailure(root, path, directoryProcessFailureReason(err), summary)
			logging.Error("remove empty source directory failed path=%s err=%v", path, err)
			continue
		}
		summary.EmptyDirectoriesRemoved++
	}
	summary.emptyDirectoryCandidates = nil
}

func directoryProcessFailureReason(err error) string {
	if err == nil {
		return "未知错误"
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "target already exists"):
		return "目标位置已存在同名文件"
	case strings.Contains(message, "existing NFO is not managed by JavBoss"):
		return "已有非 JavBoss 管理的 NFO，未覆盖该文件"
	case errors.Is(err, os.ErrPermission):
		return "没有足够的文件或目录访问权限"
	case errors.Is(err, os.ErrNotExist):
		return "源文件或相关目录不存在"
	default:
		return message
	}
}

func directoryProcessModeDisplay(mode string) string {
	switch mode {
	case DirectoryProcessSidecar:
		return "仅生成 NFO 和封面"
	case DirectoryProcessOrganize:
		return "仅整理目录"
	case DirectoryProcessOrganizeWithSidecar:
		return "整理目录并生成 NFO 和封面"
	default:
		return mode
	}
}

func directoryProcessLayoutDisplay(layout string) string {
	switch layout {
	case DirectoryProcessLayoutCode:
		return "按完整番号（JAV/番号）"
	case DirectoryProcessLayoutIdol:
		return "按女优（JAV/女优/番号）"
	default:
		return "按番号前缀（JAV/前缀/番号）"
	}
}

func reportLineValue(value string) string {
	return strings.NewReplacer("\r", `\r`, "\n", `\n`, "\t", `\t`).Replace(value)
}

func writeDirectoryProcessIssueSection(
	builder *strings.Builder,
	title string,
	sourceLabel string,
	items []DirectoryProcessIssue,
) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(builder, "\n【%s】\n", title)
	for i := range items {
		issue := items[i]
		label := strings.TrimSpace(issue.Code)
		if label == "" {
			label = filepath.Base(filepath.FromSlash(issue.SourcePath))
		}
		fmt.Fprintf(builder, "\n%d. %s\n", i+1, reportLineValue(label))
		fmt.Fprintf(builder, "%s：%s\n", sourceLabel, reportLineValue(issue.SourcePath))
		if issue.TargetPath != "" {
			fmt.Fprintf(builder, "目标文件：%s\n", reportLineValue(issue.TargetPath))
		}
		fmt.Fprintf(builder, "原因：%s\n", reportLineValue(issue.Reason))
	}
}

func writeDirectoryProcessReport(
	root string,
	mode string,
	layout string,
	summary *DirectoryProcessSummary,
	processErr error,
	startedAt time.Time,
	finishedAt time.Time,
) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("directory report root must be absolute")
	}
	if summary == nil {
		summary = &DirectoryProcessSummary{}
	}

	var builder strings.Builder
	builder.WriteString("JavBoss 目录整理报告\n\n")
	fmt.Fprintf(&builder, "开始时间：%s\n", startedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&builder, "完成时间：%s\n", finishedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&builder, "处理模式：%s\n", directoryProcessModeDisplay(mode))
	if mode != DirectoryProcessSidecar {
		fmt.Fprintf(&builder, "整理方式：%s\n", directoryProcessLayoutDisplay(layout))
	}
	if processErr != nil {
		fmt.Fprintf(&builder, "任务结果：未完成（%s）\n", reportLineValue(directoryProcessFailureReason(processErr)))
	} else {
		builder.WriteString("任务结果：已完成\n")
	}
	fmt.Fprintf(&builder, "\n参与处理视频：%d\n", summary.Locations)
	fmt.Fprintf(&builder, "成功移动：%d\n", summary.Moved)
	fmt.Fprintf(&builder, "已经位于目标位置：%d\n", summary.AlreadyOrganized)
	fmt.Fprintf(&builder, "未满足整理条件：%d\n", summary.Skipped)
	fmt.Fprintf(&builder, "移动失败并留在原处：%d\n", len(summary.MoveFailures))
	fmt.Fprintf(&builder, "NFO/封面写入成功：%d\n", summary.Sidecars)
	fmt.Fprintf(&builder, "NFO 已存在（已保留）：%d\n", summary.NFOPreserved)
	fmt.Fprintf(&builder, "封面已写入：%d\n", summary.PostersWritten)
	fmt.Fprintf(&builder, "背景图(fanart)已写入：%d\n", summary.FanartsWritten)
	fmt.Fprintf(&builder, "封面待下载：%d\n", summary.PostersPending)
	fmt.Fprintf(&builder, "NFO/封面生成失败：%d\n", len(summary.SidecarFailures))
	fmt.Fprintf(&builder, "已删除空目录：%d\n", summary.EmptyDirectoriesRemoved)
	fmt.Fprintf(&builder, "空目录清理失败：%d\n", len(summary.DirectoryCleanupFailures))

	writeDirectoryProcessIssueSection(&builder, "移动失败并留在原处", "源文件", summary.MoveFailures)
	writeDirectoryProcessIssueSection(&builder, "未满足整理条件", "源文件", summary.SkippedItems)
	writeDirectoryProcessIssueSection(&builder, "NFO/封面生成失败", "源文件", summary.SidecarFailures)
	writeDirectoryProcessIssueSection(
		&builder,
		"空目录清理失败",
		"目录",
		summary.DirectoryCleanupFailures,
	)

	reportPath := filepath.Join(root, directoryProcessReportName)
	if err := writeFileAtomically(reportPath, strings.NewReader(builder.String()), 0o644); err != nil {
		return fmt.Errorf("write processing report: %w", err)
	}
	return nil
}

func safeDirectoryFilePath(root, relative string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("directory root must be absolute")
	}
	relative = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relative)))
	if relative == "" || relative == "." || filepath.IsAbs(relative) {
		return "", errors.New("relative file path is invalid")
	}
	target := filepath.Clean(filepath.Join(root, relative))
	check, err := filepath.Rel(root, target)
	if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
		return "", errors.New("file path escapes directory root")
	}
	return target, nil
}

var attachmentExtensions = map[string]struct{}{
	".aac":  {},
	".ass":  {},
	".flac": {},
	".jpg":  {},
	".jpeg": {},
	".mka":  {},
	".mp3":  {},
	".nfo":  {},
	".png":  {},
	".ssa":  {},
	".srt":  {},
	".vtt":  {},
	".webp": {},
}

func moveMediaGroup(source, target string) (bool, error) {
	source = filepath.Clean(source)
	target = filepath.Clean(target)
	if source == target {
		info, err := os.Lstat(source)
		if err != nil {
			return false, fmt.Errorf("inspect source: %w", err)
		}
		if !info.Mode().IsRegular() {
			return false, errors.New("source is not a regular file")
		}
		return false, nil
	}

	info, err := os.Lstat(source)
	if err != nil {
		return false, fmt.Errorf("inspect source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("source is not a regular file")
	}

	files := []string{source}
	sourceDir := filepath.Dir(source)
	stem := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return false, fmt.Errorf("read source directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == filepath.Base(source) {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, stem+".") && !strings.HasPrefix(name, stem+"-") {
			continue
		}
		if _, ok := attachmentExtensions[strings.ToLower(filepath.Ext(name))]; !ok {
			continue
		}
		candidate := filepath.Join(sourceDir, name)
		candidateInfo, err := os.Lstat(candidate)
		if err != nil || !candidateInfo.Mode().IsRegular() || util.IsVideo(candidate) {
			continue
		}
		files = append(files, candidate)
	}

	targetDir := filepath.Dir(target)
	for _, file := range files {
		next := filepath.Join(targetDir, filepath.Base(file))
		if _, err := os.Lstat(next); err == nil {
			return false, fmt.Errorf("target already exists: %s", next)
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect target %s: %w", next, err)
		}
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return false, fmt.Errorf("create target directory: %w", err)
	}

	moved := make([][2]string, 0, len(files))
	for _, file := range files {
		next := filepath.Join(targetDir, filepath.Base(file))
		if err := os.Rename(file, next); err != nil {
			for i := len(moved) - 1; i >= 0; i-- {
				if rollbackErr := os.Rename(moved[i][1], moved[i][0]); rollbackErr != nil {
					logging.Error("rollback media move failed source=%s target=%s err=%v", moved[i][1], moved[i][0], rollbackErr)
				}
			}
			return false, fmt.Errorf("move %s: %w", file, err)
		}
		moved = append(moved, [2]string{file, next})
	}
	return true, nil
}

type jellyfinNFO struct {
	XMLName       xml.Name        `xml:"movie"`
	Generator     string          `xml:"generator"`
	Title         string          `xml:"title"`
	OriginalTitle string          `xml:"originaltitle,omitempty"`
	SortTitle     string          `xml:"sorttitle,omitempty"`
	UniqueID      jellyfinUnique  `xml:"uniqueid"`
	Premiered     string          `xml:"premiered,omitempty"`
	ReleaseDate   string          `xml:"releasedate,omitempty"`
	Year          int             `xml:"year,omitempty"`
	Runtime       int             `xml:"runtime,omitempty"`
	Tagline       string          `xml:"tagline,omitempty"`
	Outline       string          `xml:"outline,omitempty"`
	Mpaa          string          `xml:"mpaa,omitempty"`
	Studio        string          `xml:"studio,omitempty"`
	DateAdded     string          `xml:"dateadded,omitempty"`
	Set           *jellyfinSet    `xml:"set,omitempty"`
	Genres        []string        `xml:"genre,omitempty"`
	Tags          []string        `xml:"tag,omitempty"`
	Actors        []jellyfinActor `xml:"actor,omitempty"`
}

type jellyfinUnique struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr"`
	Value   string `xml:",chardata"`
}

type jellyfinSet struct {
	Name string `xml:"name"`
}

type jellyfinActor struct {
	Name string `xml:"name"`
}

func makeJellyfinNFO(item *models.Jav) jellyfinNFO {
	code := strings.ToUpper(strings.TrimSpace(item.Code))
	// originaltitle 只放原文标题；中文标题仅在没有原文标题时用来补 title，
	// 避免把译文写成 originaltitle 造成两个标题语义错位。
	originalTitle := strings.TrimSpace(item.Title)
	title := code
	if label := firstNonEmptyString(item.Title, item.ZhTitle); label != "" {
		title = strings.TrimSpace(code + " " + label)
	}
	result := jellyfinNFO{
		Generator:     "JavBoss",
		Title:         title,
		OriginalTitle: originalTitle,
		SortTitle:     code,
		UniqueID: jellyfinUnique{
			Type:    "javboss",
			Default: true,
			Value:   code,
		},
		Runtime: item.DurationMin,
		Mpaa:    "JP-18+",
	}
	if item.ReleaseUnix > 0 {
		released := time.Unix(item.ReleaseUnix, 0).UTC()
		result.Premiered = released.Format("2006-01-02")
		result.ReleaseDate = result.Premiered
		result.Year, _ = strconv.Atoi(released.Format("2006"))
		// 与常见刮削器的 NFO 保持一致：Jellyfin/Emby 会把 tagline/outline
		// 显示在影片副标题位置。发行日期是数据库里真实有的信息，直接推导。
		releaseLabel := "配信開始日 " + result.Premiered
		result.Tagline = releaseLabel
		result.Outline = releaseLabel
	}
	// Emby/Jellyfin 用 dateadded 排序"最新入库"，取记录写入时间最贴近实际。
	addedAt := item.CreatedAt
	if addedAt.IsZero() {
		addedAt = item.FetchedAt
	}
	if !addedAt.IsZero() {
		result.DateAdded = addedAt.Local().Format("2006-01-02 15:04:05")
	}
	if item.Studio != nil {
		result.Studio = strings.TrimSpace(item.Studio.Name)
	}
	if item.Series != nil && strings.TrimSpace(item.Series.Name) != "" {
		result.Set = &jellyfinSet{Name: strings.TrimSpace(item.Series.Name)}
	}

	seen := map[string]struct{}{}
	for _, tag := range item.Tags {
		name := strings.TrimSpace(tag.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result.Genres = append(result.Genres, name)
	}
	if item.IsUncensored != nil {
		if *item.IsUncensored {
			result.Tags = append(result.Tags, "无码")
		} else {
			result.Tags = append(result.Tags, "有码")
		}
	}
	for _, idol := range item.Idols {
		name := firstNonEmptyString(idol.ChineseName, idol.Name, idol.JapaneseName, idol.RomanName)
		if name != "" {
			result.Actors = append(result.Actors, jellyfinActor{Name: name})
		}
	}
	return result
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// sidecarResult reports what writeJavSidecars actually changed for one video.
// The NFO and the poster are handled independently so that a pre-existing NFO
// owned by another tool does not block the cover from being filled in.
type sidecarResult struct {
	// nfoWritten is true when JavBoss wrote the NFO in this run.
	nfoWritten bool
	// nfoPreserved is true when an NFO owned by another tool was left untouched.
	nfoPreserved bool
	// posterWritten is true when a poster image was copied next to the video.
	posterWritten bool
	// fanartWritten is true when a fanart image was written next to the video.
	fanartWritten bool
	// coverSkipped is true when the poster already existed and needed no update.
	coverSkipped bool
	// posterPending is true when no cover is available yet and the download was
	// only queued, so the poster must be written by a later run.
	posterPending bool
}

// changed reports whether this result represents applied work.
func (r sidecarResult) changed() bool {
	return r.nfoWritten || r.posterWritten || r.fanartWritten
}

// writeJavSidecars writes the JavBoss NFO and poster for videoPath.
//
// A missing cover is fetched synchronously within coverEnsureTimeout so the
// poster lands in the same run instead of requiring a second pass. When the
// download is not configured or times out, the request is queued and the item
// is reported as posterPending.
func writeJavSidecars(ctx context.Context, videoPath string, item *models.Jav, coverDir string) (sidecarResult, error) {
	var result sidecarResult

	info, err := os.Stat(videoPath)
	if err != nil {
		return result, fmt.Errorf("inspect video: %w", err)
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("video is not a regular file")
	}

	base := strings.TrimSuffix(videoPath, filepath.Ext(videoPath))
	nfoPath := base + ".nfo"
	owned, err := canWriteJavBossNFO(nfoPath)
	if err != nil && !errors.Is(err, errNFOUnownedByOtherTool) {
		// Unreadable NFO: still try the cover, then surface the NFO problem.
		if _, coverErr := writeJavPoster(ctx, base, item, coverDir, true, &result); coverErr != nil {
			logging.Error("write JAV poster failed path=%s code=%s err=%v", videoPath, item.Code, coverErr)
		}
		return result, err
	}
	if errors.Is(err, errNFOUnownedByOtherTool) {
		result.nfoPreserved = true
	} else {
		// 内容没变就不落盘：整理会把整个目录重跑一遍，
		// 无条件重写只会把 NFO 的 mtime 刷成当天，对用户没有任何收益。
		written, writeErr := writeNFOIfChanged(nfoPath, makeJellyfinNFO(item))
		if writeErr != nil {
			if _, coverErr := writeJavPoster(ctx, base, item, coverDir, true, &result); coverErr != nil {
				logging.Error("write JAV poster failed path=%s code=%s err=%v", videoPath, item.Code, coverErr)
			}
			return result, writeErr
		}
		result.nfoWritten = written
	}

	// Overwrite an existing poster only when JavBoss owns the accompanying NFO,
	// which keeps the side effect limited to files we are allowed to manage.
	// 注意 overwrite 只表示"允许覆盖"，真正要不要写由 writeJavPoster 里的
	// posterUpToDate 判定，因此内容没变时不会刷新 mtime。
	if _, err := writeJavPoster(ctx, base, item, coverDir, result.nfoWritten || owned, &result); err != nil {
		return result, err
	}
	return result, nil
}

// writeJavPoster copies the cached cover next to the video, fetching it first
// when it is missing. It records the outcome on result rather than deciding
// whether the run failed, because a pending cover is not an error.
func writeJavPoster(
	ctx context.Context,
	base string,
	item *models.Jav,
	coverDir string,
	overwrite bool,
	result *sidecarResult,
) (string, error) {
	coverPath, ok := cover.FindPath(coverDir, item.Code)
	if !ok {
		if skipCoverWait(ctx) {
			// 自动整理不在这里等下载：封面队列已经在后台拉取，
			// 这里只把缺失项登记进队列，交给后续轮次补写。
			// 未配置封面目录时队列未启动，登记会退化成空操作。
			if cover.Enabled() {
				cover.Enqueue(item.Code)
			}
		} else {
			coverPath, ok = ensureCover(ctx, item.Code)
		}
	}
	if !ok {
		result.posterPending = true
		return "", nil
	}

	posterPath := javPosterFileName(base, filepath.Ext(coverPath))
	info, statErr := os.Stat(posterPath)
	hasPoster := statErr == nil && info.Size() > 0
	switch {
	case statErr != nil && !errors.Is(statErr, os.ErrNotExist):
		return "", fmt.Errorf("inspect poster: %w", statErr)
	case hasPoster && !overwrite:
		// 这张海报属于别的工具（NFO 也不是 JavBoss 写的）：只接受现状，不改别人的图。
		result.coverSkipped = true
		// 即使跳过海报，也要补齐可能缺失的 fanart。
		writeJavFanart(ctx, base, item, coverPath, result)
		return posterPath, nil
	case hasPoster && posterUpToDate(coverPath, posterPath):
		// 已存在，且尺寸与「当前封面 + 当前裁剪比例」一致、封面也没更新：
		// 说明本轮无事可做，直接返回，避免重新解码编码把 mtime 刷成当天。
		result.coverSkipped = true
		writeJavFanart(ctx, base, item, coverPath, result)
		return posterPath, nil
	}
	if crop := posterCropFraction(); crop > 0 {
		if cropped, err := writeCroppedPoster(coverPath, posterPath, crop); err == nil && cropped {
			result.posterWritten = true
			writeJavFanart(ctx, base, item, coverPath, result)
			return posterPath, nil
		} else if err != nil {
			// 裁剪失败不阻断整理：回退为整图复制，保证媒体库至少有封面可用。
			logging.Error("crop poster failed, fall back to full cover path=%s code=%s crop=%.2f err=%v", posterPath, item.Code, crop, err)
		}
	}
	// 走到这里说明要么关闭了裁剪、要么裁剪不适用（非 jpeg/png 等）：
	// 整图复制也是纯派生文件，尺寸与 mtime 一致就说明已经有同一份了。
	if sidecarFileUpToDate(coverPath, posterPath) {
		result.coverSkipped = true
		writeJavFanart(ctx, base, item, coverPath, result)
		return posterPath, nil
	}
	if err := copyFileAtomically(coverPath, posterPath); err != nil {
		return "", fmt.Errorf("write poster: %w", err)
	}
	result.posterWritten = true
	writeJavFanart(ctx, base, item, coverPath, result)
	return posterPath, nil
}

// javPosterFileName 返回视频旁的海报路径，例如 /media/stars-182/stars-182.(mp4)-poster.jpg。
// base 是去掉扩展名的视频路径。Emby 的 Primary 图片优先序列里 {name}-poster.ext 排在裸名
// poster.ext 之前，且带名字的写法在一个目录放多部影片/多个版本时也不会互相覆盖。
func javPosterFileName(base, coverExt string) string {
	return base + posterSuffix + strings.ToLower(coverExt)
}

// javFanartFileName 返回视频旁的背景图路径，例如 /media/stars-182/stars-182.(mp4)-fanart.jpg。
// Emby 把 {name}-fanart.ext 与 fanart.ext 同列为 Backdrop 候选，这里用带名字的写法。
func javFanartFileName(base, coverExt string) string {
	return base + fanartSuffix + strings.ToLower(coverExt)
}

// writeJavFanart 把未裁剪的原始封面写到视频旁作为 fanart（Emby 的背景图）。
// fanart 与 poster 互补：poster 是裁掉左侧小图的竖版主图，fanart 保留整幅封套。
// 两者同源——都从 coverDir 里的原始封面派生，fanart 是母版，poster 是它的右半部分，
// 因此调大裁剪比例随时可以从 fanart 重新裁出海报，反之则不可逆。
// 任何失败都只记录日志，不阻断整理流程。
func writeJavFanart(ctx context.Context, base string, item *models.Jav, coverPath string, result *sidecarResult) {
	if err := ctx.Err(); err != nil {
		return
	}
	if !fanartEnabled() {
		return
	}
	target := javFanartFileName(base, filepath.Ext(coverPath))
	if info, err := os.Stat(target); err == nil && info.Size() > 0 {
		// 已存在且非空：只有封面比 fanart 更新时才重写。
		// 这样既不每轮重刷大图，也不会让背景图永远停在旧封面上。
		coverInfo, statErr := os.Stat(coverPath)
		if statErr != nil || !coverInfo.ModTime().After(info.ModTime()) {
			return
		}
		logging.Info("fanart refreshed after cover update path=%s code=%s", target, item.Code)
	}
	if err := copyFileAtomically(coverPath, target); err != nil {
		logging.Error("write JAV fanart failed path=%s code=%s err=%v", target, item.Code, err)
		return
	}
	result.fanartWritten = true
}

// fanartEnabled 控制是否在视频旁写出 fanart。默认开启，可用环境变量关闭：
//
//	JAVBOSS_POSTER_FANART=0  不生成 fanart
func fanartEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JAVBOSS_POSTER_FANART"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// posterCropFraction 返回写入媒体库海报时要裁掉的左侧宽度占比。
// JAV 封面多为 DVD 封套（左侧小图 + 右侧竖版主图），Emby/Jellyfin 的海报位
// 需要的是右侧竖版主图，因此默认裁掉左侧 53%。环境变量可覆盖：
//
//	JAVBOSS_POSTER_CROP_LEFT=0     关闭裁剪，直接复制整张封面
//	JAVBOSS_POSTER_CROP_LEFT=0.53  裁掉左侧 53%（默认）
func posterCropFraction() float64 {
	raw := strings.TrimSpace(os.Getenv("JAVBOSS_POSTER_CROP_LEFT"))
	if raw == "" {
		return defaultPosterCropLeft
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return defaultPosterCropLeft
	}
	if value <= 0 {
		return 0
	}
	if value >= maxPosterCropLeft {
		return maxPosterCropLeft
	}
	return value
}

// writeCroppedPoster 把封面裁掉左侧 crop 比例后写成海报。
// 返回 false 表示该格式不支持裁剪（调用方应回退为整图复制）。
// 是否真的需要裁由 writeJavPoster 的 posterUpToDate 先判定，这里只管执行。
func writeCroppedPoster(coverPath, posterPath string, crop float64) (bool, error) {
	src, err := os.Open(coverPath)
	if err != nil {
		return false, fmt.Errorf("open cover: %w", err)
	}
	defer src.Close()

	img, format, err := image.Decode(src)
	if err != nil {
		return false, fmt.Errorf("decode cover: %w", err)
	}
	if format != "jpeg" && format != "png" {
		return false, nil
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return false, errors.New("cover has no pixels")
	}
	cut := int(math.Round(float64(width) * crop))
	if cut <= 0 {
		return false, nil
	}
	if cut >= width {
		return false, fmt.Errorf("crop %.2f removes the whole cover", crop)
	}

	// 拷贝到 RGBA 再编码：SubImage 视图类型多样（YCbCr/Paletted/NRGBA…），
	// 直接交给 jpeg.Encode 的路径不统一，先归一化到 RGBA 最稳。
	rect := image.Rect(bounds.Min.X+cut, bounds.Min.Y, bounds.Max.X, bounds.Max.Y)
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), img, rect.Min, draw.Src)

	tmp := posterPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return false, fmt.Errorf("create poster: %w", err)
	}
	encodeErr := jpeg.Encode(out, dst, &jpeg.Options{Quality: 92})
	closeErr := out.Close()
	if encodeErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if encodeErr != nil {
			return false, fmt.Errorf("encode poster: %w", encodeErr)
		}
		return false, fmt.Errorf("close poster: %w", closeErr)
	}
	if err := os.Rename(tmp, posterPath); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("commit poster: %w", err)
	}
	return true, nil
}

// ensureCover waits a bounded time for the cover of code to become available.
// When downloads are disabled or the wait times out, it falls back to queueing
// the download so a later run can complete the poster.
func ensureCover(ctx context.Context, code string) (string, bool) {
	waitCtx, cancel := context.WithTimeout(ctx, coverEnsureTimeout)
	defer cancel()

	path, err := cover.Ensure(waitCtx, code)
	if err == nil {
		return path, true
	}
	if !errors.Is(err, cover.ErrDownloadsDisabled) {
		logging.Info("cover not available yet, queued for later: code=%s err=%v", code, err)
	}
	cover.Enqueue(code)
	return "", false
}

func canWriteJavBossNFO(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read existing NFO: %w", err)
	}
	if !strings.Contains(string(data), "<generator>JavBoss</generator>") {
		return false, errNFOUnownedByOtherTool
	}
	return true, nil
}

// encodeNFO 生成 NFO 的最终字节。抽出来是为了让「内容有没有变」可以先比较再落盘。
func encodeNFO(value jellyfinNFO) ([]byte, error) {
	var builder strings.Builder
	builder.WriteString(xml.Header)
	encoder := xml.NewEncoder(&builder)
	encoder.Indent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode NFO: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("finish NFO: %w", err)
	}
	return []byte(builder.String()), nil
}

func writeNFOFile(path string, value jellyfinNFO) error {
	data, err := encodeNFO(value)
	if err != nil {
		return err
	}
	return writeFileAtomically(path, bytes.NewReader(data), 0o644)
}

// writeNFOIfChanged 只在内容真的变化时才落盘，返回是否写了文件。
//
// 整理会反复处理同一个目录，旧实现无条件重写，导致已经刮削好的 NFO
// 每次都被盖上当天的 mtime（Emby/Jellyfin 侧看起来像"刚刮削"）。
// 这里做两级比较：先比字节（绝大多数情况直接命中），再按字段比内容——
// 数据库里标签/演员没有稳定排序，只有顺序抖动时不应触发重写。
func writeNFOIfChanged(path string, value jellyfinNFO) (bool, error) {
	data, err := encodeNFO(value)
	if err != nil {
		return false, err
	}
	existing, readErr := os.ReadFile(path)
	if readErr == nil {
		if bytes.Equal(existing, data) {
			return false, nil
		}
		var parsed jellyfinNFO
		if xml.Unmarshal(existing, &parsed) == nil && nfoEquivalent(parsed, value) {
			return false, nil
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return false, fmt.Errorf("read existing NFO: %w", readErr)
	}
	if err := writeFileAtomically(path, bytes.NewReader(data), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// nfoEquivalent 按字段比较两份 NFO，忽略体裁/标签/演员的排列顺序。
func nfoEquivalent(left, right jellyfinNFO) bool {
	if left.Generator != right.Generator ||
		left.Title != right.Title ||
		left.OriginalTitle != right.OriginalTitle ||
		left.SortTitle != right.SortTitle ||
		left.UniqueID != right.UniqueID ||
		left.Premiered != right.Premiered ||
		left.ReleaseDate != right.ReleaseDate ||
		left.Year != right.Year ||
		left.Runtime != right.Runtime ||
		left.Tagline != right.Tagline ||
		left.Outline != right.Outline ||
		left.Mpaa != right.Mpaa ||
		left.Studio != right.Studio ||
		left.DateAdded != right.DateAdded ||
		!equalUnorderedStrings(left.Genres, right.Genres) ||
		!equalUnorderedStrings(left.Tags, right.Tags) ||
		!equalUnorderedActors(left.Actors, right.Actors) {
		return false
	}
	switch {
	case left.Set == nil && right.Set == nil:
		return true
	case left.Set == nil || right.Set == nil:
		return false
	default:
		return left.Set.Name == right.Set.Name
	}
}

func equalUnorderedStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for i := range leftCopy {
		if leftCopy[i] != rightCopy[i] {
			return false
		}
	}
	return true
}

func equalUnorderedActors(left, right []jellyfinActor) bool {
	if len(left) != len(right) {
		return false
	}
	leftNames := make([]string, 0, len(left))
	for _, actor := range left {
		leftNames = append(leftNames, actor.Name)
	}
	rightNames := make([]string, 0, len(right))
	for _, actor := range right {
		rightNames = append(rightNames, actor.Name)
	}
	return equalUnorderedStrings(leftNames, rightNames)
}

// sidecarFileUpToDate 判断「由 source 原样复制出来的 target」是否已经是最新的：
// 尺寸相同且 target 不比 source 旧。纯 stat 判定，不读文件内容。
func sidecarFileUpToDate(sourcePath, targetPath string) bool {
	source, err := os.Stat(sourcePath)
	if err != nil || !source.Mode().IsRegular() || source.Size() == 0 {
		return false
	}
	target, err := os.Stat(targetPath)
	if err != nil || target.Size() != source.Size() {
		return false
	}
	return !target.ModTime().Before(source.ModTime())
}

// posterUpToDate 报告现有海报是否已经是「当前封面 + 当前裁剪比例」的产物。
//
// 判定只看文件属性与图片头，不把像素解出来、更不重新编码：
//   - 尺寸比对：封面被我方更新过、或裁剪比例改过，尺寸就不再匹配；
//   - mtime 比对：封面被重新下载后，旧海报必须重裁。
//
// 因此每个条目的开销是 2 次 stat + 1 次图片头读取（几十字节），
// 真正的解码/编码只在判定为过期之后才发生。
func posterUpToDate(coverPath, posterPath string) bool {
	coverInfo, err := os.Stat(coverPath)
	if err != nil || !coverInfo.Mode().IsRegular() {
		return false
	}
	posterInfo, err := os.Stat(posterPath)
	if err != nil || posterInfo.Size() <= 0 {
		return false
	}
	if posterInfo.ModTime().Before(coverInfo.ModTime()) {
		return false
	}
	wantWidth, wantHeight, ok := expectedPosterDimensions(coverPath, posterCropFraction())
	if !ok {
		return false
	}
	gotWidth, gotHeight, ok := imageDimensions(posterPath)
	if !ok {
		return false
	}
	return wantWidth == gotWidth && wantHeight == gotHeight
}

// expectedPosterDimensions 预测写出的海报尺寸，判定规则与 writeCroppedPoster 保持一致。
func expectedPosterDimensions(coverPath string, crop float64) (int, int, bool) {
	file, err := os.Open(coverPath)
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	config, format, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0, false
	}
	width, height := config.Width, config.Height
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	// 下面这些分支都会回退成"整图复制"，预期尺寸就是封面原尺寸。
	if format != "jpeg" && format != "png" {
		return width, height, true
	}
	if crop <= 0 {
		return width, height, true
	}
	cut := int(math.Round(float64(width) * crop))
	if cut <= 0 || cut >= width {
		return width, height, true
	}
	return width - cut, height, true
}

func imageDimensions(path string) (int, int, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0, false
	}
	if config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

func copyFileAtomically(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	return writeFileAtomically(target, input, 0o644)
}

func writeFileAtomically(target string, input io.Reader, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(target), ".javboss-sidecar-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if _, err := io.Copy(temp, input); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	renameErr := os.Rename(tempPath, target)
	if renameErr == nil {
		return nil
	}
	if _, err := os.Stat(target); err != nil {
		return renameErr
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, target)
}
