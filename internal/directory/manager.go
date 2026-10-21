package directory

import (
	"context"
	"sync"
	"time"
)

type Options struct {
	CoverDir string
	// AutoProcessMode 开启后，扫描发现“已刮削但未写 NFO”的视频会自动整理。
	// 取值同目录处理模式：sidecar / organize / organize_with_sidecar；空表示关闭。
	AutoProcessMode string
	// AutoProcessCoverWait 是自动整理前等待封面队列排空的上限，零值用默认值。
	AutoProcessCoverWait time.Duration
}

// Manager coordinates directory tasks and owns their shared progress and cancellation state.
type Manager struct {
	coverDir string
	ctx      context.Context

	autoProcessMode      string
	autoProcessCoverWait time.Duration
	// autoQueue 保存扫描完成、等待自动整理的目录。
	autoQueue *autoProcessQueue

	dirScanMu                 sync.Mutex
	dirScanActive             map[int64]*directoryScanSession
	directoryProcessingMu     sync.Mutex
	directoryProcessingStatus map[int64]string
}

func New(options Options) *Manager {
	return &Manager{
		ctx:                       context.Background(),
		coverDir:                  options.CoverDir,
		autoProcessMode:           options.AutoProcessMode,
		autoProcessCoverWait:      options.AutoProcessCoverWait,
		autoQueue:                 newAutoProcessQueue(),
		dirScanActive:             make(map[int64]*directoryScanSession),
		directoryProcessingStatus: make(map[int64]string),
	}
}
