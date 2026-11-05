package sitebuilds

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// setStartErr makes the kind's runner refuse to start, as a step whose image did not pull would.
func (b *testBed) setStartErr(kind string, err error) {
	if kind == KindApp {
		b.apps.startErr = err
		return
	}
	b.sites.startErr = err
}

// A build image that did not pull fails as FailImage, worded for the user, with its token revoked and its Job removed.
func TestBuild_AnImagePullAtStartFailsAsImage(t *testing.T) {
	for _, kind := range []string{KindSite, KindApp} {
		bed := newTestBed(true)
		bed.setStartErr(kind, fmt.Errorf("%w: pull registry.internal:5000/node:22: manifest unknown", ErrImagePull))
		queued := bed.queue(t, kind)
		bed.runAll()
		done := bed.store.build(queued.ID)
		if done.Status != StatusFailed || done.FailureKind != FailImage {
			t.Errorf("%s: %+v, want FAILED with %q", kind, done, FailImage)
		}
		if strings.Contains(done.FailureReason, "registry.internal") || strings.Contains(done.FailureReason, "manifest") {
			t.Errorf("%s: the user message shows the registry: %q", kind, done.FailureReason)
		}
		if revoked := bed.provider.revokedTokens(); !slices.Equal(revoked, []string{"clone-token-1"}) {
			t.Errorf("%s: revoked %v, want the minted token", kind, revoked)
		}
		if removed := bed.jobsOf(kind).removedIDs(); !slices.Equal(removed, []string{queued.ID}) {
			t.Errorf("%s: removed %v, want the build's Job", kind, removed)
		}
	}
}

// A build whose clone credential could not be minted started no Job, so nothing reads its logs or removes it.
func TestBuild_NoJobStartedIsNeverRemoved(t *testing.T) {
	for _, kind := range []string{KindSite, KindApp} {
		bed := newTestBed(true)
		bed.provider.mintErr = errors.New("GitHub answered 502")
		queued := bed.queue(t, kind)
		bed.runAll()
		if done := bed.store.build(queued.ID); done.Status != StatusFailed {
			t.Errorf("%s: %+v", kind, done)
		}
		jobs := bed.jobsOf(kind)
		if removed := jobs.removedIDs(); len(removed) != 0 || jobs.logReads != 0 {
			t.Errorf("%s: removed %v and read logs %d times for a Job that never existed", kind, removed, jobs.logReads)
		}
	}
}
