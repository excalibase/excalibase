package domain

import (
	"testing"
	"time"
)

func TestRestoreTargetKind(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name       string
		r          RestoreRequest
		kind, want string
	}{
		{"latest", RestoreRequest{}, "latest", ""},
		{"time", RestoreRequest{TargetTime: &FlexTime{Time: ts}}, "time", "2026-01-02T03:04:05Z"},
		{"xid", RestoreRequest{TargetXID: "100"}, "xid", "100"},
		{"lsn", RestoreRequest{TargetLSN: "0/ABCDEF"}, "lsn", "0/ABCDEF"},
		{"name", RestoreRequest{TargetName: "snap1"}, "name", "snap1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, v := c.r.RestoreTargetKind()
			if k != c.kind || v != c.want {
				t.Errorf("RestoreTargetKind() = (%q,%q), want (%q,%q)", k, v, c.kind, c.want)
			}
		})
	}
}

func TestRestoreRequestValidate(t *testing.T) {
	ts := &FlexTime{Time: time.Now()}
	cases := []struct {
		name    string
		r       RestoreRequest
		wantErr bool
	}{
		{"latest with name", RestoreRequest{NewProjectName: "p"}, false},
		{"single target with id", RestoreRequest{TargetXID: "1", NewProjectID: "id"}, false},
		{"two targets rejected", RestoreRequest{TargetTime: ts, TargetXID: "1", NewProjectName: "p"}, true},
		{"missing new project", RestoreRequest{TargetXID: "1"}, true},
		{"all empty", RestoreRequest{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.r.Validate(); (err != nil) != c.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestRestoreRecoveryTarget(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := (RestoreRequest{}).RecoveryTarget(); got != nil {
		t.Errorf("latest should be nil, got %v", got)
	}
	cases := []struct {
		name     string
		r        RestoreRequest
		key, val string
	}{
		{"time", RestoreRequest{TargetTime: &FlexTime{Time: ts}}, "targetTime", "2026-01-02T03:04:05Z"},
		{"xid", RestoreRequest{TargetXID: "7"}, "targetXID", "7"},
		{"lsn", RestoreRequest{TargetLSN: "0/1"}, "targetLSN", "0/1"},
		{"name", RestoreRequest{TargetName: "s"}, "targetName", "s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.r.RecoveryTarget()[c.key]; got != c.val {
				t.Errorf("RecoveryTarget()[%q] = %v, want %q", c.key, got, c.val)
			}
		})
	}
}

func TestGetNewProjectPriority(t *testing.T) {
	if got := (RestoreRequest{NewProjectName: "name", NewProjectID: "id"}).GetNewProject(); got != "name" {
		t.Errorf("name should win, got %q", got)
	}
	if got := (RestoreRequest{NewProjectID: "id"}).GetNewProject(); got != "id" {
		t.Errorf("id fallback, got %q", got)
	}
}

func TestWALGEnv(t *testing.T) {
	if got := (&DatabaseInstance{}).WALGEnv(); len(got) != 0 {
		t.Errorf("WALGEnv should be empty, got %v", got)
	}
}

func TestIsValidTier(t *testing.T) {
	for _, tier := range []TierType{Free, Standard, Enterprise} {
		if !IsValidTier(tier) {
			t.Errorf("%s should be valid", tier)
		}
	}
	if IsValidTier(TierType("BOGUS")) {
		t.Error("BOGUS should be invalid")
	}
}
