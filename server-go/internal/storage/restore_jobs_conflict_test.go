package storage

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestRestoreJobsConflict(t *testing.T) {
	copyAt := func(source, value string) *domain.RestoreJob {
		return &domain.RestoreJob{SourceProjectID: source, Mode: domain.RestoreModeNewProject, TargetKind: "time", TargetValue: value}
	}
	inPlace := &domain.RestoreJob{SourceProjectID: "p1", Mode: domain.RestoreModeInPlace, TargetKind: "latest"}
	for name, c := range map[string]struct {
		running, next *domain.RestoreJob
		want          bool
	}{
		"another project":            {copyAt("p1", "t1"), copyAt("p2", "t1"), false},
		"the same copy twice":        {copyAt("p1", "t1"), copyAt("p1", "t1"), true},
		"copies to different points": {copyAt("p1", "t1"), copyAt("p1", "t2"), false},
		"a copy while replacing":     {inPlace, copyAt("p1", "t2"), true},
		"a replace while copying":    {copyAt("p1", "t1"), inPlace, true},
	} {
		if got := RestoreJobsConflict(c.running, c.next); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}
