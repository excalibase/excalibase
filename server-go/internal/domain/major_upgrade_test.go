package domain

import (
	"errors"
	"testing"
)

func TestMajorUpgradeRequestValidate(t *testing.T) {
	cases := []struct {
		name string
		req  MajorUpgradeRequest
		want error
	}{
		{"confirmed major", MajorUpgradeRequest{TargetVersion: "17", Confirm: true}, nil},
		{"spaces are trimmed", MajorUpgradeRequest{TargetVersion: " 16 ", Confirm: true}, nil},
		{"not confirmed", MajorUpgradeRequest{TargetVersion: "17"}, ErrMajorUpgradeUnconfirmed},
		{"no target", MajorUpgradeRequest{Confirm: true}, ErrMajorUpgradeTarget},
		{"a minor is not a major", MajorUpgradeRequest{TargetVersion: "17.2", Confirm: true}, ErrMajorUpgradeTarget},
		{"an image is not a major", MajorUpgradeRequest{TargetVersion: "postgres:17", Confirm: true}, ErrMajorUpgradeTarget},
		{"too long", MajorUpgradeRequest{TargetVersion: "12345", Confirm: true}, ErrMajorUpgradeTarget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.req.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMajorUpgradeRequestTarget(t *testing.T) {
	if got := (MajorUpgradeRequest{TargetVersion: " 17 "}).Target(); got != "17" {
		t.Fatalf("Target() = %q, want 17", got)
	}
}

func TestBackupArchiveName(t *testing.T) {
	cases := []struct{ original, major, want string }{
		{"16", "16", ""},
		{"16", "17", "pg17"},
		{"14", "15", "pg15"},
		{"", "17", ""},
		{"16", "", ""},
	}
	for _, tc := range cases {
		if got := BackupArchiveName(tc.original, tc.major); got != tc.want {
			t.Errorf("BackupArchiveName(%q, %q) = %q, want %q", tc.original, tc.major, got, tc.want)
		}
	}
}

func TestMajorUpgradeFinished(t *testing.T) {
	for status, want := range map[string]bool{
		MajorUpgradeRunning:    false,
		MajorUpgradeCompleted:  true,
		MajorUpgradeRolledBack: true,
		MajorUpgradeFailed:     true,
	} {
		if got := (MajorUpgrade{Status: status}).Finished(); got != want {
			t.Errorf("Finished() for %s = %v, want %v", status, got, want)
		}
	}
}
