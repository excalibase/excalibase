package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Studio's document browser logs in to the gateway with a login of its own,
// a Mongo user the platform creates and never shows (EXC-410), so no platform
// Postgres role has to be reachable on the Mongo port.

type docBrowserHarness struct {
	svc   *ProvisioningService
	kube  *k8s.MockClient
	vault *fakeVault
}

func newDocBrowserHarness(t *testing.T) *docBrowserHarness {
	t.Helper()
	inst := documentDBProject()
	kube := k8s.NewMockClient()
	cluster := mongoCluster()
	cluster.SetName(inst.ProjectID + "-postgres")
	cluster.SetNamespace(inst.Namespace)
	if err := kube.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, inst.Namespace, cluster); err != nil {
		t.Fatal(err)
	}
	kube.ExecOutput[inst.Namespace+"/"+inst.ProjectID+"-postgres-1"] = "t\n"
	vault := newFakeVault()
	svc := NewProvisioningService(documentDBStore(t), provisioner.NewFactory(), kube)
	svc.SetOrgStore(testOrgs())
	svc.SetVault(vault)
	svc.mongoIdentWait = identWait{timeout: 50 * time.Millisecond, interval: time.Millisecond}
	return &docBrowserHarness{svc: svc, kube: kube, vault: vault}
}

func (h *docBrowserHarness) enable(t *testing.T) error {
	t.Helper()
	return h.svc.enableDocumentDB(context.Background(), documentDBProject(), idleContext())
}

func (h *docBrowserHarness) peerLines(t *testing.T) []string {
	t.Helper()
	inst := documentDBProject()
	cluster, err := h.kube.GetCRD(context.Background(), k8s.CNPGClusterGVR, inst.Namespace, inst.ProjectID+"-postgres")
	if err != nil {
		t.Fatal(err)
	}
	lines, _, _ := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_ident")
	return lines
}

func TestEnableDocumentDBCreatesTheDocumentBrowserLogin(t *testing.T) {
	h := newDocBrowserHarness(t)
	if err := h.enable(t); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}
	sql := strings.Join(h.kube.ExecStdin, "\n")
	names := decodedLiterals(sql)
	if !slices.Contains(names, DocBrowserLogin) {
		t.Fatalf("no statement creates %s: %q", DocBrowserLogin, names)
	}
	if !strings.Contains(sql, "CREATE ROLE %I WITH LOGIN PASSWORD") || !strings.Contains(sql, "excalibase_mongo_users") {
		t.Errorf("the login is not a Mongo user: %s", sql)
	}
	record, err := h.vault.Get(docBrowserVaultPath(documentDBProject().ProjectID))
	if err != nil {
		t.Fatalf("no vault record: %v", err)
	}
	if record["username"] != DocBrowserLogin || len(record["password"]) != mongoUserPasswordLength {
		t.Errorf("record = %v", redactPassword(record))
	}
	if strings.Contains(sql, record["password"]) {
		t.Error("the password itself reached the database")
	}
	if !slices.Contains(h.peerLines(t), "local postgres "+DocBrowserLogin) {
		t.Errorf("the login is not peer-mapped: %q", h.peerLines(t))
	}
}

// The login is filed beside the platform's credentials, not among the
// project's own Mongo users, so no customer listing shows it.
func TestTheDocumentBrowserLoginIsNotACustomerMongoUser(t *testing.T) {
	path := docBrowserVaultPath("proj-x")
	if strings.HasPrefix(path, mongoUserVaultPrefix("proj-x")) || !strings.HasPrefix(path, vaultCredentialPrefix("proj-x")) {
		t.Errorf("path %s", path)
	}
	if ValidateMongoUsername(DocBrowserLogin) == nil {
		t.Error("a customer could create or delete the platform's login")
	}
}

// A restored cluster arrives with the source's Mongo users, this login among
// them; they are dropped first and the login is created afresh with a new
// password.
func TestTheLoginIsCreatedAfterTheSourcesMongoUsersAreDropped(t *testing.T) {
	h := newDocBrowserHarness(t)
	if err := h.enable(t); err != nil {
		t.Fatal(err)
	}
	dropAt := slices.IndexFunc(h.kube.ExecCommands, func(cmd string) bool { return strings.Contains(cmd, "excalibase_mongo_users' AND NOT u.rolsuper LOOP") })
	createAt := slices.IndexFunc(h.kube.ExecCommands, func(cmd string) bool { return strings.HasSuffix(cmd, "-f -") })
	if dropAt < 0 || createAt < 0 || dropAt > createAt {
		t.Errorf("drop at %d, create at %d: %q", dropAt, createAt, h.kube.ExecCommands)
	}
}

func TestTheLoginIsUndoneWhenItCannotBeMapped(t *testing.T) {
	h := newDocBrowserHarness(t)
	inst := documentDBProject()
	h.kube.ExecOutput[inst.Namespace+"/"+inst.ProjectID+"-postgres-1"] = "f\n"
	if err := h.enable(t); err == nil {
		t.Fatal("a login that cannot reach its session was kept")
	}
	if _, err := h.vault.Get(docBrowserVaultPath(inst.ProjectID)); err == nil {
		t.Error("the login was filed anyway")
	}
	if slices.Contains(h.peerLines(t), "local postgres "+DocBrowserLogin) {
		t.Error("the peer line was left behind")
	}
}

func TestTheLoginIsDroppedWhenItCannotBeFiled(t *testing.T) {
	h := newDocBrowserHarness(t)
	h.vault.putErr = errors.New("vault sealed")
	if err := h.enable(t); err == nil {
		t.Fatal("an unfiled login was reported as created")
	}
	if !strings.Contains(strings.Join(h.kube.ExecStdin, "\n"), "DROP ROLE") {
		t.Error("the unfiled role was not dropped")
	}
}

func TestRegistrationUndoesTheLoginRecordOnFailure(t *testing.T) {
	h := newDocBrowserHarness(t)
	pc := idleContext()
	if err := h.svc.enableDocumentDB(context.Background(), documentDBProject(), pc); err != nil {
		t.Fatal(err)
	}
	pc.Rollback(context.Background())
	if _, err := h.vault.Get(docBrowserVaultPath(documentDBProject().ProjectID)); err == nil {
		t.Error("a rolled-back registration left the login filed")
	}
}

// excalibase_app logs in by certificate only; it is no longer a DocumentDB user.
func TestThePlatformAppRoleGetsNoDocumentDBAccess(t *testing.T) {
	h := newDocBrowserHarness(t)
	if err := h.enable(t); err != nil {
		t.Fatal(err)
	}
	if grants := appRoleGrants(h.kube); len(grants) != 0 {
		t.Errorf("excalibase_app granted DocumentDB access: %q", grants)
	}
}

func redactPassword(record map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range record {
		if key == "password" {
			value = "***"
		}
		out[key] = value
	}
	return out
}
