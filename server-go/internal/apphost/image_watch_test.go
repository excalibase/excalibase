package apphost

import (
	"strings"
	"testing"
)

func TestWatchesImage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	for name, tc := range map[string]struct {
		app  App
		want bool
	}{
		"a running app watching a tag":       {App{AutoDeploy: true, Image: "ghcr.io/a/web:main", Status: StatusRunning}, true},
		"a failed app watching a tag":        {App{AutoDeploy: true, Image: "ghcr.io/a/web:main", Status: StatusFailed}, true},
		"not opted in":                       {App{Image: "ghcr.io/a/web:main", Status: StatusRunning}, false},
		"pinned by digest":                   {App{AutoDeploy: true, Image: "ghcr.io/a/web@" + digest, Status: StatusRunning}, false},
		"stopped: a deploy would restart it": {App{AutoDeploy: true, Image: "ghcr.io/a/web:main", Status: StatusStopped}, false},
		"never deployed":                     {App{AutoDeploy: true, Image: "ghcr.io/a/web:main", Status: StatusCreated}, false},
		"being deleted":                      {App{AutoDeploy: true, Image: "ghcr.io/a/web:main", Status: StatusDeleting}, false},
	} {
		if got := tc.app.WatchesImage(); got != tc.want {
			t.Errorf("%s: WatchesImage = %v, want %v", name, got, tc.want)
		}
	}
}

func TestIsPinnedByDigest(t *testing.T) {
	if !IsPinnedByDigest("ghcr.io/a/web@sha256:" + strings.Repeat("ab", 32)) {
		t.Error("a digest reference is pinned")
	}
	if IsPinnedByDigest("localhost:5000/web:1") {
		t.Error("a tag is not pinned")
	}
}
