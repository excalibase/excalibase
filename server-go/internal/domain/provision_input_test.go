package domain

import (
	"strings"
	"testing"
	"time"
)

func TestValidateMasterUsernameAcceptsPlainRoleNames(t *testing.T) {
	for _, name := range []string{"app", "owner_doc", "_svc", "a", "u" + strings.Repeat("x", 62)} {
		if err := ValidateMasterUsername(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
}

// The name lands in pg_hba trust lines, so anything that could add a token
// there (spaces, '#', newlines, keywords) is refused.
func TestValidateMasterUsernameRefusesHBATokensAndReservedNames(t *testing.T) {
	for _, name := range []string{
		"all 0.0.0.0/0 trust #", "app\nhost", "App", "1app", "a-b", "a b", "",
		"u" + strings.Repeat("x", 63),
		"postgres", "all", "replication", "sameuser", "samerole",
		"streaming_replica", "cnpg_pooler_pgbouncer", "pg_monitor", "pg_anything",
	} {
		if err := ValidateMasterUsername(name); err == nil {
			t.Errorf("%q: accepted", name)
		}
	}
}

func TestNormalizeProjectNameCountsCharacters(t *testing.T) {
	if got, ok := NormalizeProjectName("  blog  "); !ok || got != "blog" {
		t.Errorf("got %q %v", got, ok)
	}
	if _, ok := NormalizeProjectName(strings.Repeat("é", 100)); !ok {
		t.Error("100 two-byte characters refused")
	}
	for _, name := range []string{"", "   ", strings.Repeat("a", 101)} {
		if _, ok := NormalizeProjectName(name); ok {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestRestoreValidateRefusesTargetsThatCouldEscapeTheConfig(t *testing.T) {
	bad := []RestoreRequest{
		{TargetXID: "1'\narchive_command = 'sh -c id"},
		{TargetXID: "-1"},
		{TargetXID: "12a"},
		{TargetLSN: "0/1'\narchive_command='x'"},
		{TargetLSN: "0/"},
		{TargetLSN: "123456789/1"},
		{TargetName: "snap'\nx"},
		{TargetName: "has space"},
		{TargetName: strings.Repeat("a", 64)},
	}
	for _, r := range bad {
		r.NewProjectName = "restored"
		if err := r.Validate(); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	good := []RestoreRequest{
		{TargetXID: "12345"},
		{TargetLSN: "0/16B3748"},
		{TargetLSN: "ABCDEF01/ff"},
		{TargetName: "before-migration_2.1"},
		{TargetTime: &ZonedTime{Time: time.Now()}},
	}
	for _, r := range good {
		r.NewProjectName = "restored"
		if err := r.Validate(); err != nil {
			t.Errorf("refused %+v: %v", r, err)
		}
	}
}

func TestRestoreValidateAppliesTheProjectNameRule(t *testing.T) {
	for _, name := range []string{"   ", strings.Repeat("a", 101)} {
		if err := (RestoreRequest{NewProjectName: name}).Validate(); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
	if err := (RestoreRequest{NewProjectName: strings.Repeat("é", 100)}).Validate(); err != nil {
		t.Errorf("100 characters refused: %v", err)
	}
}
