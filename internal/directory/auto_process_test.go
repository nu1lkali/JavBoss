package directory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"javboss/internal/models"
)

func TestNormalizeAutoProcessMode(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "", want: ""},
		{raw: "   ", want: ""},
		{raw: "sidecar", want: DirectoryProcessSidecar},
		{raw: "SIDECAR", want: DirectoryProcessSidecar},
		{raw: " sidecar ", want: DirectoryProcessSidecar},
		{raw: "organize", want: DirectoryProcessOrganize},
		{raw: "organize_with_sidecar", want: DirectoryProcessOrganizeWithSidecar},
		{raw: "bogus", want: ""},
		{raw: "1", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			if got := normalizeAutoProcessMode(tc.raw); got != tc.want {
				t.Fatalf("normalizeAutoProcessMode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestJavSidecarCompleteRequiresNFOAndPoster(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "stars-182.strm")
	if err := os.WriteFile(video, []byte("http://example.test/stars-182.mp4"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}

	// 没有 NFO：未整理。
	if javSidecarComplete(video, true) {
		t.Fatal("video without NFO should be reported as incomplete")
	}

	// 第三方 NFO：依旧视为未整理，整理流程会保留它并只补封面。
	if err := os.WriteFile(filepath.Join(dir, "stars-182.nfo"), []byte("<movie><title>x</title></movie>"), 0o644); err != nil {
		t.Fatalf("write foreign nfo: %v", err)
	}
	if javSidecarComplete(video, true) {
		t.Fatal("foreign NFO should not count as JavBoss-processed")
	}

	// JavBoss 自己的 NFO，但封面还没下载好：仍算未整理。
	// 这是 writeJavSidecars 先写 NFO、后写封面的直接后果，
	// 只看 NFO 会让缺图条目永远补不上封面。
	owned := "<movie><generator>JavBoss</generator></movie>"
	if err := os.WriteFile(filepath.Join(dir, "stars-182.nfo"), []byte(owned), 0o644); err != nil {
		t.Fatalf("write owned nfo: %v", err)
	}
	if javSidecarComplete(video, true) {
		t.Fatal("owned NFO without poster should still be incomplete")
	}
	// 不要求海报时（未启用封面下载）只看 NFO。
	if !javSidecarComplete(video, false) {
		t.Fatal("owned NFO should count as processed when poster check is disabled")
	}

	// 海报落盘后才算完成。
	poster := filepath.Join(dir, "stars-182-poster.jpg")
	if err := os.WriteFile(poster, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatalf("write poster: %v", err)
	}
	if !javSidecarComplete(video, true) {
		t.Fatal("owned NFO plus poster should count as processed")
	}

	// 空文件不算有效海报。
	if err := os.WriteFile(poster, nil, 0o644); err != nil {
		t.Fatalf("truncate poster: %v", err)
	}
	if javSidecarComplete(video, true) {
		t.Fatal("empty poster file should not count as processed")
	}
}

func TestJavSidecarCompleteHandlesDoubleExtension(t *testing.T) {
	dir := t.TempDir()
	// STRM 常见写法：视频名里带格式标记，NFO 与图片仍按“去掉最后一段扩展名”命名。
	video := filepath.Join(dir, "MIGD-641.(mkv).strm")
	if err := os.WriteFile(video, []byte("http://example.test/migd-641.mkv"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	nfo := filepath.Join(dir, "MIGD-641.(mkv).nfo")
	if err := os.WriteFile(nfo, []byte("<movie><generator>JavBoss</generator></movie>"), 0o644); err != nil {
		t.Fatalf("write nfo: %v", err)
	}
	poster := filepath.Join(dir, "MIGD-641.(mkv)-poster.jpg")
	if err := os.WriteFile(poster, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatalf("write poster: %v", err)
	}
	if !javSidecarComplete(video, true) {
		t.Fatal("NFO plus poster written by JavBoss should be detected for double-extension names")
	}
}

func TestQueueAutoProcessNoopWhenDisabled(t *testing.T) {
	manager := New(Options{CoverDir: ""})
	dir := models.Directory{ID: 1, Path: t.TempDir()}
	// 未配置模式时不应做任何探测或入队。
	manager.queueAutoProcess(dir)
	if got := manager.autoQueue.len(); got != 0 {
		t.Fatalf("queued jobs = %d, want 0", got)
	}
	if status := manager.DirectoryWorkStatus(dir.ID); status != DirectoryWorkIdle {
		t.Fatalf("work status = %q, want idle", status)
	}
}

func TestQueueAutoProcessSkipsDeletedOrMissing(t *testing.T) {
	manager := New(Options{CoverDir: "", AutoProcessMode: DirectoryProcessSidecar})
	path := t.TempDir()

	// 这三个都会在触碰数据库之前返回，因此测试不需要初始化全局 store。
	for _, dir := range []models.Directory{
		{ID: 1, Path: path, IsDelete: true},
		{ID: 2, Path: path, Missing: true},
		{ID: 0, Path: path},
	} {
		manager.queueAutoProcess(dir)
		if status := manager.DirectoryWorkStatus(dir.ID); status != DirectoryWorkIdle {
			t.Fatalf("dir %+v: work status = %q, want idle", dir, status)
		}
	}
	if got := manager.autoQueue.len(); got != 0 {
		t.Fatalf("queued jobs = %d, want 0", got)
	}
}

func TestAutoProcessQueueDeduplicatesByDirectoryID(t *testing.T) {
	queue := newAutoProcessQueue()
	dir := models.Directory{ID: 7, Path: "/media/av"}

	if !queue.put(&autoProcessJob{directory: dir, mode: DirectoryProcessSidecar, queuedAt: time.Now()}) {
		t.Fatal("first put should be accepted")
	}
	if queue.put(&autoProcessJob{directory: dir, mode: DirectoryProcessSidecar, queuedAt: time.Now()}) {
		t.Fatal("duplicate put should be rejected so a rescan does not restart the wait")
	}
	if got := queue.len(); got != 1 {
		t.Fatalf("queued jobs = %d, want 1", got)
	}

	queue.remove(dir.ID)
	if got := queue.len(); got != 0 {
		t.Fatalf("queued jobs after remove = %d, want 0", got)
	}
}

func TestAutoProcessQueueSnapshotOrdersByQueueTime(t *testing.T) {
	queue := newAutoProcessQueue()
	base := time.Now()
	queue.put(&autoProcessJob{directory: models.Directory{ID: 2}, queuedAt: base.Add(2 * time.Second)})
	queue.put(&autoProcessJob{directory: models.Directory{ID: 1}, queuedAt: base})

	jobs := queue.snapshot()
	if len(jobs) != 2 {
		t.Fatalf("snapshot size = %d, want 2", len(jobs))
	}
	if jobs[0].directory.ID != 1 || jobs[1].directory.ID != 2 {
		t.Fatalf("snapshot order = [%d %d], want [1 2]", jobs[0].directory.ID, jobs[1].directory.ID)
	}
}

func TestSkipCoverWaitContext(t *testing.T) {
	if skipCoverWait(context.Background()) {
		t.Fatal("plain context should not skip cover wait")
	}
	if !skipCoverWait(withSkipCoverWait(context.Background())) {
		t.Fatal("context marked by withSkipCoverWait should skip cover wait")
	}
}

func TestParseConfigBool(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "true", want: true},
		{raw: "TRUE", want: true},
		{raw: " 1 ", want: true},
		{raw: "yes", want: true},
		{raw: "on", want: true},
		{raw: "false", want: false},
		{raw: "0", want: false},
		{raw: "", want: false},
		{raw: "随便", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			if got := parseConfigBool(tc.raw); got != tc.want {
				t.Fatalf("parseConfigBool(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestResolveAutoProcessMode(t *testing.T) {
	tests := []struct {
		name       string
		envMode    string
		enabled    bool
		configured bool
		want       string
	}{
		// 环境变量没开启时，设置页开关无法突破。
		{name: "env off", envMode: "", enabled: true, configured: true, want: ""},
		// 从未写过开关：跟随环境变量。
		{name: "env on, switch unset", envMode: DirectoryProcessSidecar, want: DirectoryProcessSidecar},
		{name: "env on, switch unset (organized)", envMode: DirectoryProcessOrganize, want: DirectoryProcessOrganize},
		// 显式打开。
		{name: "switch on", envMode: DirectoryProcessSidecar, enabled: true, configured: true, want: DirectoryProcessSidecar},
		// 显式关闭：这是网页上点“关”之后的唯一正确结果。
		{name: "switch off", envMode: DirectoryProcessSidecar, enabled: false, configured: true, want: ""},
		// 未知模式视为关闭，避免拼错的值被当成已开启。
		{name: "unknown env value", envMode: "bogus", enabled: true, configured: true, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveAutoProcessMode(tc.envMode, tc.enabled, tc.configured); got != tc.want {
				t.Fatalf("resolveAutoProcessMode(%q, %v, %v) = %q, want %q",
					tc.envMode, tc.enabled, tc.configured, got, tc.want)
			}
		})
	}
}

func TestAutoProcessQueueClearDropsAllJobs(t *testing.T) {
	queue := newAutoProcessQueue()
	queue.put(&autoProcessJob{directory: models.Directory{ID: 1}, queuedAt: time.Now()})
	queue.put(&autoProcessJob{directory: models.Directory{ID: 2}, queuedAt: time.Now()})

	if dropped := queue.clear(); dropped != 2 {
		t.Fatalf("clear() = %d, want 2", dropped)
	}
	if got := queue.len(); got != 0 {
		t.Fatalf("queued jobs after clear = %d, want 0", got)
	}
}
