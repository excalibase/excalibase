//go:build integration

package provisioner

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-593: on a real engine, a backed-up project's closed WAL lands in its
// archive directory and the archiver reports no failure.
func TestDockerPostgresArchivesClosedWALOnARealEngine(t *testing.T) {
	docker := requireDocker(t)
	defer docker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	project := fmt.Sprintf("it-arch-%d", time.Now().UnixNano()%1_000_000)
	p := NewDockerPostgreSQLProvisioner(docker)
	req := archivedRequest(project)
	result, err := p.Provision(ctx, req, testTier, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = p.Deprovision(context.Background(), DatabaseContainerName(project), project) })
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	psql := func(sql string) (string, error) {
		return docker.ExecInContainerStdin(ctx, result.Namespace, []string{"psql", "-U", "postgres", "-tAc", sql}, "")
	}
	if _, err := psql("CREATE TABLE notes AS SELECT 1 AS id; SELECT pg_switch_wal()"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		out, err := psql("SELECT archived_count || ' ' || failed_count FROM pg_stat_archiver")
		out = strings.TrimSpace(out)
		if err == nil && out != "" && out[0] != '0' {
			if !strings.HasSuffix(out, " 0") {
				t.Fatalf("archiver failures: %q", out)
			}
			if code, _ := docker.ExecInContainer(ctx, result.Namespace, []string{"sh", "-c", "ls /walarchive | grep -qE '^[0-9A-F]{24}$'"}); code != 0 {
				t.Fatalf("archiver counts %q but no segment in /walarchive", out)
			}
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("no WAL segment archived")
}
