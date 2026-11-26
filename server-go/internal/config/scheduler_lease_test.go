package config

import (
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/scheduler"
)

// A lease shorter than one sweep, one invocation and the write that records
// it lets a healthy slow function be reclaimed and run a second time.
func TestValidateRefusesAClaimLeaseThatCanExpireDuringARun(t *testing.T) {
	cfg := AppConfig{
		SchedulerEnabled:        true,
		SchedulerProjectTimeout: 30 * time.Second,
	}
	floor := cfg.SchedulerProjectTimeout + scheduler.InvokeTimeout + scheduler.OutcomeTimeout

	cfg.SchedulerClaimLease = floor
	err := cfg.validateScheduler()
	if err == nil || !strings.Contains(err.Error(), "EXCALIBASE_SCHEDULER_CLAIM_LEASE_MS") {
		t.Fatalf("a lease equal to the worst case run must be refused, got %v", err)
	}
	cfg.SchedulerClaimLease = floor + time.Second
	if err := cfg.validateScheduler(); err != nil {
		t.Fatalf("a lease past the worst case run must pass, got %v", err)
	}
	cfg.SchedulerEnabled = false
	cfg.SchedulerClaimLease = time.Second
	if err := cfg.validateScheduler(); err != nil {
		t.Fatalf("a disabled scheduler has no lease to check, got %v", err)
	}
}

func TestDefaultsPassTheClaimLeaseCheck(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://app.excalibase.io")
	if err := Load().validateScheduler(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
}
