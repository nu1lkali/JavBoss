package directory

import (
	"context"
	"sync/atomic"
	"time"

	"javboss/internal/models"
)

var applicationTasks atomic.Pointer[Manager]

func init() { Init(context.Background(), "") }

// Init configures directory tasks before consumers start. Previous tasks must have stopped.
func Init(ctx context.Context, coverDir string) {
	m := New(Options{CoverDir: coverDir})
	m.ctx = ctx
	applicationTasks.Store(m)
}

// InitWithOptions 与 Init 相同，但允许传入自动整理等额外配置。
func InitWithOptions(ctx context.Context, options Options) {
	m := New(options)
	m.ctx = ctx
	applicationTasks.Store(m)
}

// Context is the application lifetime for directory jobs that outlive HTTP requests.
func Context() context.Context { return applicationTasks.Load().ctx }

func DirectoryWorkSnapshot(id int64) (string, *DirectoryScanProgress) {
	return applicationTasks.Load().DirectoryWorkSnapshot(id)
}

func DirectoryWorkStatus(id int64) string { return applicationTasks.Load().DirectoryWorkStatus(id) }

func IsDirectoryScanning(id int64) bool { return applicationTasks.Load().IsDirectoryScanning(id) }

func ScanDirectory(ctx context.Context, dir models.Directory) (*Summary, error) {
	return applicationTasks.Load().ScanDirectory(ctx, dir)
}

func StartManualDirectoryScan(dir models.Directory) error {
	return applicationTasks.Load().StartManualDirectoryScan(dir)
}

func CancelAndReserveDirectoryScan(ctx context.Context, id int64) (func(), error) {
	return applicationTasks.Load().CancelAndReserveDirectoryScan(ctx, id)
}

func StartDirectoryProcessing(ctx context.Context, dir models.Directory, mode, layout string) error {
	return applicationTasks.Load().StartDirectoryProcessing(ctx, dir, mode, layout)
}

// QueueAutoProcess 在扫描结束后登记一次自动整理。
// 未开启自动整理时为空操作；开启时也只是入队，不会阻塞调用方。
func QueueAutoProcess(dir models.Directory) {
	applicationTasks.Load().queueAutoProcess(dir)
}

// QueueAutoProcessForAll 让所有可用目录重新评估一次自动整理。
// 设置页把开关打开后调用，立刻补齐存量目录，不必等下一轮扫描。
func QueueAutoProcessForAll(ctx context.Context) {
	applicationTasks.Load().queueAutoProcessForAll(ctx)
}

// StartAutoProcessScheduler 启动自动整理的后台 worker，按间隔检查待整理目录。
func StartAutoProcessScheduler(ctx context.Context, interval time.Duration) {
	applicationTasks.Load().StartAutoProcessScheduler(ctx, interval)
}

func StartAutomaticDirectoryScanScheduler(ctx context.Context, interval time.Duration) {
	applicationTasks.Load().StartAutomaticDirectoryScanScheduler(ctx, interval)
}
