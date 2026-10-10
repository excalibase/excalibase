package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A DocumentDB project on a single host (EXC-576) has the same Mongo users
// and document browser login as on Kubernetes; their statements run in the
// project's database container, over exec stdin.

const singleHostContainer = "excalibase-proj-docsvc001-postgres"

func singleHostDocumentDBProject() *domain.DatabaseInstance {
	inst := documentDBProject()
	inst.DeploymentMode, inst.Namespace, inst.Username, inst.Status = domain.ModeDocker, singleHostContainer, "postgres", "ACTIVE"
	return inst
}

type singleHostHarness struct {
	svc    *ProvisioningService
	docker *recordingDocker
	vault  *fakeVault
}

func newSingleHostHarness(t *testing.T) *singleHostHarness {
	t.Helper()
	store := documentDBStore(t)
	if err := store.Create(singleHostDocumentDBProject()); err != nil {
		t.Fatal(err)
	}
	docker := newRecordingDocker()
	vault := newFakeVault()
	svc := NewProvisioningService(store, provisioner.NewFactory(), nil)
	svc.SetDockerClient(docker)
	svc.SetVault(vault)
	return &singleHostHarness{svc: svc, docker: docker, vault: vault}
}

func (h *singleHostHarness) stdinIn(container string) string {
	var statements []string
	for _, fed := range h.docker.stdin {
		if fed.container == container {
			statements = append(statements, string(fed.body))
		}
	}
	return strings.Join(statements, "\n")
}

func TestASingleHostDocumentDBProjectGetsTheDocumentBrowserLogin(t *testing.T) {
	h := newSingleHostHarness(t)
	if err := h.svc.enableDocumentDB(context.Background(), singleHostDocumentDBProject(), idleContext()); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}
	sql := h.stdinIn(singleHostContainer)
	if !slices.Contains(decodedLiterals(sql), DocBrowserLogin) || !strings.Contains(sql, "excalibase_mongo_users") {
		t.Fatalf("no Mongo user statement for %s in the database container: %q", DocBrowserLogin, sql)
	}
	record, err := h.vault.Get(docBrowserVaultPath("proj-docsvc001"))
	if err != nil || record["username"] != DocBrowserLogin || record["password"] == "" {
		t.Fatalf("vault record %v err %v", redactPassword(record), err)
	}
}

func TestMongoUsersOnASingleHostRunInTheDatabaseContainer(t *testing.T) {
	h := newSingleHostHarness(t)
	ctx := context.Background()
	created, err := h.svc.CreateMongoUser(ctx, "proj-docsvc001", "reporting", MongoRoleRead)
	if err != nil {
		t.Fatalf("CreateMongoUser: %v", err)
	}
	if created.Password == "" || !slices.Contains(decodedLiterals(h.stdinIn(singleHostContainer)), "reporting") {
		t.Fatalf("created %+v, statements %q", created, h.stdinIn(singleHostContainer))
	}
	users, err := h.svc.ListMongoUsers(ctx, "proj-docsvc001")
	if err != nil || len(users) != 1 || users[0].Username != "reporting" {
		t.Fatalf("users %v err %v", users, err)
	}
	if _, err := h.svc.RotateMongoUser(ctx, "proj-docsvc001", "reporting"); err != nil {
		t.Fatalf("RotateMongoUser: %v", err)
	}
	if err := h.svc.DeleteMongoUser(ctx, "proj-docsvc001", "reporting"); err != nil {
		t.Fatalf("DeleteMongoUser: %v", err)
	}
	if users, _ := h.svc.ListMongoUsers(ctx, "proj-docsvc001"); len(users) != 0 {
		t.Fatalf("still listed: %v", users)
	}
}

func TestMongoUsersNeedAnEngineAndAVault(t *testing.T) {
	svc := NewProvisioningService(documentDBStore(t), provisioner.NewFactory(), nil)
	svc.SetVault(newFakeVault())
	if _, err := svc.ListMongoUsers(context.Background(), "proj-docsvc001"); !errors.Is(err, ErrMongoUsersUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// A single host runs no CDC watcher (registration deploys none), so a resume
// has no replication to restart rather than failing on it.
func TestASingleHostProjectHasNoReplicationToRestart(t *testing.T) {
	h := newSingleHostHarness(t)
	if err := h.svc.RestartReplication(context.Background(), singleHostDocumentDBProject()); err != nil {
		t.Fatalf("RestartReplication: %v", err)
	}
	k8sProject := documentDBProject()
	if err := h.svc.RestartReplication(context.Background(), k8sProject); !errors.Is(err, ErrReplicationRestartUnavailable) {
		t.Fatalf("a Kubernetes project without a watcher provisioner: %v", err)
	}
}
