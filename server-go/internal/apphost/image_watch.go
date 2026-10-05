package apphost

import (
	"strings"
	"time"
)

// ImageWatch is what the image watcher last saw of an app's tag (EXC-542).
type ImageWatch struct {
	Digest    string    `json:"digest,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	// Error is why the last check did not get an answer, in words safe to show.
	Error string `json:"error,omitempty"`
}

// ImageWatchStore is what the image watcher reads and writes.
type ImageWatchStore interface {
	// ListAutoDeploy returns every app, across projects, that opted in.
	ListAutoDeploy() ([]*App, error)
	// RecordImageWatch is observed state: it never moves the app's version.
	RecordImageWatch(projectID, id string, watch ImageWatch) error
}

var _ ImageWatchStore = (*PostgresAppStore)(nil)

func IsPinnedByDigest(image string) bool {
	return strings.Contains(image, "@")
}

// WatchesImage reports whether the watcher follows the app's tag now: only an
// app that opted in, names a tag, and runs or failed. A deploy of a stopped
// app would start it, and one never deployed waits for its first deploy.
func (a *App) WatchesImage() bool {
	if !a.AutoDeploy || IsPinnedByDigest(a.Image) {
		return false
	}
	return a.Status == StatusRunning || a.Status == StatusFailed
}
