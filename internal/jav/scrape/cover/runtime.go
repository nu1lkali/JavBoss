package cover

import (
	"context"
	"errors"
	"sync/atomic"
)

var active atomic.Pointer[queue]

// Start configures and starts cover downloads. Call once before starting consumers.
// An empty directory disables background downloads.
func Start(ctx context.Context, coverDir string) {
	q := newQueue(coverDir)
	active.Store(q)
	q.Start(ctx)
}

// Enqueue schedules a cover download, or does nothing when downloads are disabled.
func Enqueue(code string) { active.Load().Enqueue(code) }

// Exists reports whether a valid cover is cached in the configured directory.
// Use FindPath to look in a directory without starting background downloads.
func Exists(code string) bool {
	q := active.Load()
	return q != nil && exists(q.coverDir, code)
}

// Enabled reports whether background cover downloads are configured.
func Enabled() bool { return active.Load() != nil }

// PendingCount reports how many cover downloads are queued or in flight.
// Callers that must write posters for many items at once can wait for this to
// settle instead of relying on Ensure's per-item timeout.
func PendingCount() int { return active.Load().pendingCount() }

// Ensure downloads the cover for code if it is not cached yet and reports the
// cached path. Unlike Enqueue it waits for the result, so callers that must
// write a poster on the spot can rely on the returned path.
func Ensure(ctx context.Context, code string) (string, error) {
	q := active.Load()
	if q == nil {
		return "", ErrDownloadsDisabled
	}
	code = normalizeCode(code)
	if code == "" {
		return "", errors.New("empty code")
	}
	if err := q.downloadNow(ctx, code); err != nil {
		return "", err
	}
	if path, ok := FindPath(q.coverDir, code); ok {
		return path, nil
	}
	return "", errCoverNotFound
}
