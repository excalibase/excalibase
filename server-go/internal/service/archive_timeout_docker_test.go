package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// archiveDocker answers SHOW archive_timeout from a table and records every statement it is sent.
type archiveDocker struct {
	provisioner.DockerClient
	current map[string]string
	sent    []string
	err     error
}

func (d *archiveDocker) ExecInContainerStdin(_ context.Context, id string, _ []string, stdin string) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	d.sent = append(d.sent, id+": "+stdin)
	if strings.HasPrefix(stdin, "SHOW") {
		return d.current[id] + "\n", nil
	}
	if strings.Contains(stdin, "SET archive_timeout = '1h'") {
		d.current[id] = "1h"
	}
	return "", nil
}

func (d *archiveDocker) alters() (n int) {
	for _, sent := range d.sent {
		if strings.Contains(sent, "ALTER SYSTEM SET archive_timeout") {
			n++
		}
	}
	return n
}

func hostArchiveSvc(t *testing.T, flags features.Flags, docker *archiveDocker, projects ...*domain.DatabaseInstance) *ProvisioningService {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range projects {
		if err := store.Create(project); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewProvisioningService(store, nil, nil)
	svc.SetDockerClient(docker)
	svc.SetFeatures(flags)
	return svc
}

func hostArchivedProject(id string, tier domain.TierType, status string) *domain.DatabaseInstance {
	backups := true
	return &domain.DatabaseInstance{ProjectID: id, OrgID: "org", Namespace: "ctr-" + id, DBType: domain.PostgreSQL,
		DeploymentMode: domain.ModeDocker, Tier: tier, Status: status, BackupEnabled: &backups}
}

// EXC-784: single-host Free projects made before the guard get the hourly upload on the next reconcile pass.
func TestReconcileArchiveTimeouts_SingleHostFreeMovesToHourlyWithoutARestart(t *testing.T) {
	docker := &archiveDocker{current: map[string]string{"ctr-free": "5min"}}
	svc := hostArchiveSvc(t, features.NewStatic(features.FreeWALUpload), docker, hostArchivedProject("free", domain.Free, "ACTIVE"))

	report := svc.ReconcileArchiveTimeouts(context.Background())
	if len(report.Failed) != 0 || report.Updated != 1 || docker.alters() != 1 {
		t.Fatalf("report = %+v, alters %d", report, docker.alters())
	}
	if again := svc.ReconcileArchiveTimeouts(context.Background()); again.Updated != 0 {
		t.Fatalf("second pass changed %d, want 0", again.Updated)
	}
}

func TestReconcileArchiveTimeouts_SingleHostLeavesOtherPlansPausedProjectsAndTheGuardOffAlone(t *testing.T) {
	cases := map[string]struct {
		flags   features.Flags
		project *domain.DatabaseInstance
	}{
		"standard plan": {features.NewStatic(features.FreeWALUpload), hostArchivedProject("p", domain.Standard, "ACTIVE")},
		"paused":        {features.NewStatic(features.FreeWALUpload), hostArchivedProject("p", domain.Free, string(domain.StatusPaused))},
		"guard off":     {features.NewStatic(), hostArchivedProject("p", domain.Free, "ACTIVE")},
		"no backups": {features.NewStatic(features.FreeWALUpload), func() *domain.DatabaseInstance {
			p := hostArchivedProject("p", domain.Free, "ACTIVE")
			p.BackupEnabled = nil
			return p
		}()},
	}
	for name, tc := range cases {
		docker := &archiveDocker{current: map[string]string{"ctr-p": "5min"}}
		svc := hostArchiveSvc(t, tc.flags, docker, tc.project)
		if report := svc.ReconcileArchiveTimeouts(context.Background()); report.Updated != 0 || len(docker.sent) != 0 {
			t.Errorf("%s: report %+v, sent %v", name, report, docker.sent)
		}
	}
}

func TestReconcileArchiveTimeouts_SingleHostReportsAContainerItCannotReach(t *testing.T) {
	docker := &archiveDocker{err: errors.New("no such container")}
	svc := hostArchiveSvc(t, features.NewStatic(features.FreeWALUpload), docker, hostArchivedProject("free", domain.Free, "ACTIVE"))
	if report := svc.ReconcileArchiveTimeouts(context.Background()); len(report.Failed) != 1 || report.Updated != 0 {
		t.Fatalf("report = %+v", report)
	}
}
