package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// engineLogs stands in for the engine's log of one container.
type engineLogs struct {
	container string
	tail      int
	since     time.Time
	out       string
	err       error
}

func (e *engineLogs) Logs(_ context.Context, container string, tail int, since time.Time) (string, error) {
	e.container, e.tail, e.since = container, tail, since
	return e.out, e.err
}

func singleHostLogService(t *testing.T, engine domain.DatabaseType) *ProvisioningService {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "host-a", OrgID: "org1", Namespace: "c0ffee",
		DBType: engine, DeploymentMode: domain.ModeDocker, Status: "ACTIVE", Tier: domain.Free}); err != nil {
		t.Fatal(err)
	}
	return NewProvisioningService(store, provisioner.NewFactory(), nil)
}

// EXC-670: with host monitoring on, a single host searches what the engine
// kept of the database container's output, as the pod log does on Kubernetes.
func TestLogSearchOnASingleHostReadsTheDatabaseContainer(t *testing.T) {
	for _, engine := range []domain.DatabaseType{domain.PostgreSQL, domain.MySQL} {
		t.Run(string(engine), func(t *testing.T) {
			svc := singleHostLogService(t, engine)
			logs := &engineLogs{out: "LOG:  ready\nERROR:  relation \"x\" does not exist\nLOG:  checkpoint\n"}
			svc.SetSingleHostLogs(logs)

			page, err := svc.SearchProjectLogs(context.Background(), "host-a", LogSearch{Lines: 50, Term: "RELATION", Since: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if page.Lines != "ERROR:  relation \"x\" does not exist\n" {
				t.Errorf("lines = %q", page.Lines)
			}
			if logs.container != "c0ffee" || logs.tail != 50 || time.Since(logs.since) < 59*time.Minute || time.Since(logs.since) > 61*time.Minute {
				t.Errorf("read %s tail %d since %v", logs.container, logs.tail, logs.since)
			}
		})
	}
}

func TestLogSearchOnASingleHostClampsLikeThePodLog(t *testing.T) {
	svc := singleHostLogService(t, domain.PostgreSQL)
	logs := &engineLogs{}
	svc.SetSingleHostLogs(logs)
	if _, err := svc.SearchProjectLogs(context.Background(), "host-a", LogSearch{Lines: 99999}); err != nil {
		t.Fatal(err)
	}
	if logs.tail != 100 || !logs.since.IsZero() {
		t.Errorf("tail %d since %v, want 100 and the whole log", logs.tail, logs.since)
	}
}

func TestLogSearchOnASingleHostFailsClosed(t *testing.T) {
	svc := singleHostLogService(t, domain.PostgreSQL)
	svc.SetSingleHostLogs(&engineLogs{err: errors.New("engine proxy: 403")})
	if _, err := svc.SearchProjectLogs(context.Background(), "host-a", LogSearch{Lines: 10}); !errors.Is(err, ErrLogsUnavailable) {
		t.Errorf("err = %v, want ErrLogsUnavailable", err)
	}
	if _, err := svc.SearchProjectLogs(context.Background(), "host-a", LogSearch{Lines: 10, Term: "bad\x00term"}); !errors.Is(err, ErrInvalidLogSearch) {
		t.Errorf("err = %v, want the term refused before the engine is read", err)
	}
}
