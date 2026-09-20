package config

import (
	"testing"
	"time"
)

// Every bound a hostile tenant database can be held against is an operator
// setting, so each has to be readable from the environment and to arrive
// with a usable default.
func TestLoadReadsTheSweepAndConnectionTimeouts(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.excalibase.io")
	t.Setenv("EXCALIBASE_SCHEDULER_PROJECT_TIMEOUT_MS", "12000")
	t.Setenv("EXCALIBASE_PROJECT_DB_STATEMENT_TIMEOUT_MS", "9000")
	t.Setenv("EXCALIBASE_PROJECT_DB_LOCK_TIMEOUT_MS", "2000")

	cfg := Load()
	if cfg.SchedulerProjectTimeout != 12*time.Second {
		t.Errorf("SchedulerProjectTimeout: got %v, want 12s", cfg.SchedulerProjectTimeout)
	}
	if cfg.ProjectDBStatementTimeout != 9*time.Second {
		t.Errorf("ProjectDBStatementTimeout: got %v, want 9s", cfg.ProjectDBStatementTimeout)
	}
	if cfg.ProjectDBLockTimeout != 2*time.Second {
		t.Errorf("ProjectDBLockTimeout: got %v, want 2s", cfg.ProjectDBLockTimeout)
	}
}

func TestLoadDefaultsTheSweepAndConnectionTimeouts(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.excalibase.io")

	cfg := Load()
	if cfg.SchedulerProjectTimeout != defaultSchedulerProjectTimeout {
		t.Errorf("SchedulerProjectTimeout: got %v, want %v", cfg.SchedulerProjectTimeout, defaultSchedulerProjectTimeout)
	}
	if cfg.ProjectDBStatementTimeout != defaultProjectDBStatementTimeout {
		t.Errorf("ProjectDBStatementTimeout: got %v, want %v", cfg.ProjectDBStatementTimeout, defaultProjectDBStatementTimeout)
	}
	if cfg.ProjectDBLockTimeout != defaultProjectDBLockTimeout {
		t.Errorf("ProjectDBLockTimeout: got %v, want %v", cfg.ProjectDBLockTimeout, defaultProjectDBLockTimeout)
	}
}

// A claimed row is only recoverable if the lease that frees it is an
// operator setting with a default longer than an invocation.
func TestLoadReadsTheSchedulerClaimLease(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.excalibase.io")
	if got := Load().SchedulerClaimLease; got != defaultSchedulerClaimLease {
		t.Errorf("SchedulerClaimLease: got %v, want %v", got, defaultSchedulerClaimLease)
	}
	t.Setenv("EXCALIBASE_SCHEDULER_CLAIM_LEASE_MS", "120000")
	if got := Load().SchedulerClaimLease; got != 2*time.Minute {
		t.Errorf("SchedulerClaimLease: got %v, want 2m", got)
	}
}
