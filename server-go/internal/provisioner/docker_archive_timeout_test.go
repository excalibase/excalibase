package provisioner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func archiveTimeoutAlter(docker *mockDockerClient) string {
	for _, cmd := range docker.execLog {
		if joined := strings.Join(cmd, " "); strings.Contains(joined, "archive_timeout") {
			return joined
		}
	}
	return ""
}

// EXC-784: a single-host Free project ships its idle WAL hourly, like a Kubernetes one.
func TestDockerPostgresArchiveTimeoutFollowsThePlan(t *testing.T) {
	for name, want := range map[string]string{"1h": "archive_timeout = '1h'", "": "archive_timeout = '5min'"} {
		docker := newMockDocker()
		tier := testTier
		tier.ArchiveTimeout = name
		if _, err := NewDockerPostgreSQLProvisioner(docker).Provision(context.Background(), archivedRequest("shop"), tier, func(domain.ProvisioningStage) {}); err != nil {
			t.Fatalf("Provision: %v", err)
		}
		if got := archiveTimeoutAlter(docker); !strings.Contains(got, want) {
			t.Errorf("plan timeout %q: settings %q lack %q", name, got, want)
		}
	}
}

func TestArchiveTimeoutOfFallsBackToTheOperatorsDefault(t *testing.T) {
	if got := ArchiveTimeoutOf(testTier); got != "5min" {
		t.Errorf("no plan timeout = %q, want the 5min default", got)
	}
	tier := testTier
	tier.ArchiveTimeout = "1h"
	if got := ArchiveTimeoutOf(tier); got != "1h" {
		t.Errorf("plan timeout = %q, want 1h", got)
	}
}

// A running project's setting is read, and rewritten and reloaded only when it differs: no restart.
func TestDockerPostgresSetArchiveTimeoutChangesOnlyWhatDiffers(t *testing.T) {
	docker := newMockDocker()
	p := NewDockerPostgreSQLProvisioner(docker)

	docker.stdinOut = "5min\n"
	changed, err := p.SetArchiveTimeout(context.Background(), "c1", "1h")
	if err != nil || !changed {
		t.Fatalf("changed = %v, err %v; want a change", changed, err)
	}
	last := docker.stdinLog[len(docker.stdinLog)-1]
	if !strings.Contains(last, "ALTER SYSTEM SET archive_timeout = '1h'") || !strings.Contains(last, "pg_reload_conf()") {
		t.Fatalf("applied %q", last)
	}
	if docker.stops != 0 || docker.starts != 0 {
		t.Fatal("the container was restarted for a reloadable setting")
	}

	docker.stdinLog = nil
	docker.stdinOut = "1h\n"
	changed, err = p.SetArchiveTimeout(context.Background(), "c1", "1h")
	if err != nil || changed {
		t.Fatalf("changed = %v, err %v; want no change", changed, err)
	}
	if len(docker.stdinLog) != 1 || strings.Contains(docker.stdinLog[0], "ALTER") {
		t.Fatalf("a project already at 1h was written: %q", docker.stdinLog)
	}
}

func TestDockerPostgresSetArchiveTimeoutReportsAFailure(t *testing.T) {
	docker := newMockDocker()
	docker.stdinErr = errors.New("exec failed")
	if _, err := NewDockerPostgreSQLProvisioner(docker).SetArchiveTimeout(context.Background(), "c1", "1h"); err == nil {
		t.Fatal("a failed read was not reported")
	}
}
