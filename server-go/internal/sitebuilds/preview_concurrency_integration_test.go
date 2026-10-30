//go:build integration

package sitebuilds_test

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

// claimProject claims what the queue lets run now for the harness's project; other tests' builds are put back.
func claimProject(t *testing.T, h *sitebuildstest.Harness, holder string) []*sitebuilds.Build {
	t.Helper()
	var ours []*sitebuilds.Build
	for range 20 {
		claimed, err := h.Store.ClaimNext(context.Background(), 1000, holder, time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if claimed == nil {
			break
		}
		if claimed.ProjectID == h.Project {
			ours = append(ours, claimed)
		}
	}
	return ours
}

// A flood of previews runs one at a time per project and never holds back the production branch.
func TestPreviewBuildsRunOneAtATimePerProjectBesideProduction(t *testing.T) {
	h := previewHarness(t, sitebuildstest.Options{}, sitebuilds.PreviewModeBranches)
	ctx := context.Background()
	for i, branch := range []string{"feat/a", "feat/b"} {
		if _, err := h.Push(branch, []string{featSHA, prSHA}[i], "d-conc-"+branch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Push("main", prNewSHA, "d-conc-main"); err != nil {
		t.Fatal(err)
	}
	holder := "replica-conc-" + h.Project
	running := claimProject(t, h, holder)
	var previews, production int
	var preview *sitebuilds.Build
	for _, build := range running {
		if build.PreviewID != "" {
			previews++
			preview = build
		} else {
			production++
		}
	}
	if previews != 1 || production != 1 {
		t.Fatalf("running = %d previews and %d production builds; want one preview beside the production build", previews, production)
	}
	preview.Status = sitebuilds.StatusSucceeded
	if err := h.Store.FinishBuild(ctx, preview, holder); err != nil {
		t.Fatal(err)
	}
	next := claimProject(t, h, holder)
	if len(next) != 1 || next[0].PreviewID == "" || next[0].PreviewID == preview.PreviewID {
		t.Fatalf("next = %+v; want the other preview once the first finished", next)
	}
}
