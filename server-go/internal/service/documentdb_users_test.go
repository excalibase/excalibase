package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Mongo users a DocumentDB project creates for itself (EXC-427).

const (
	mongoProject   = "proj-mongo001"
	mongoNamespace = "org-doc-proj-mongo001"
	// Not instance 1: after a switchover the primary is whichever CNPG reports.
	mongoPrimary = mongoNamespace + "/" + mongoProject + "-postgres-2"
	mongoOwner   = "owner_doc"
)

type mongoUsersHarness struct {
	svc   *ProvisioningService
	store storage.InstanceStore
	vault *fakeVault
	kube  *k8s.MockClient
}

func newMongoUsersHarness(t *testing.T) *mongoUsersHarness {
	t.Helper()
	store := documentDBStore(t)
	inst := documentDBProject()
	inst.ProjectID, inst.Namespace, inst.Username, inst.Status = mongoProject, mongoNamespace, mongoOwner, "ACTIVE"
	if err := store.Create(inst); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	kube := k8s.NewMockClient()
	if err := kube.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, mongoNamespace, mongoCluster()); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	// The primary reports the new peer line loaded.
	kube.ExecOutput[mongoPrimary] = "t\n"
	vault := newFakeVault()
	svc := NewProvisioningService(store, provisioner.NewFactory(), kube)
	svc.SetVault(vault)
	svc.mongoIdentWait = identWait{timeout: 50 * time.Millisecond, interval: time.Millisecond}
	return &mongoUsersHarness{svc: svc, store: store, vault: vault, kube: kube}
}

// mongoCluster is a DocumentDB cluster as CNPG holds it: the platform's peer
// lines and a primary that is not instance 1.
func mongoCluster() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":     "Cluster",
		"metadata": map[string]interface{}{"name": mongoProject + "-postgres", "namespace": mongoNamespace},
		"spec": map[string]interface{}{
			"bootstrap": map[string]interface{}{"initdb": map[string]interface{}{"owner": mongoOwner}},
			"postgresql": map[string]interface{}{"pg_ident": []interface{}{
				"local postgres documentdb_bg_worker_role", "local postgres documentdb",
				"local postgres " + mongoOwner, "local postgres excalibase_app",
			}},
		},
		"status": map[string]interface{}{"currentPrimary": mongoProject + "-postgres-2"},
	}}
}

func (h *mongoUsersHarness) peerLines(t *testing.T) []string {
	t.Helper()
	cluster, err := h.kube.GetCRD(context.Background(), k8s.CNPGClusterGVR, mongoNamespace, mongoProject+"-postgres")
	if err != nil {
		t.Fatal(err)
	}
	lines, _, _ := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_ident")
	return lines
}

// stdinSQL decodes every hex literal in the recorded stdin payloads, so a
// test can read the names the statements act on.
func (h *mongoUsersHarness) stdinSQL() string {
	return strings.Join(h.kube.ExecStdin, "\n")
}

func decodedLiterals(sql string) []string {
	var out []string
	rest := sql
	for {
		start := strings.Index(rest, "decode('")
		if start < 0 {
			return out
		}
		rest = rest[start+len("decode('"):]
		end := strings.Index(rest, "'")
		raw, err := hex.DecodeString(rest[:end])
		if err == nil {
			out = append(out, string(raw))
		}
		rest = rest[end:]
	}
}

func TestMongoUsernameValidation(t *testing.T) {
	for _, name := range []string{"reporting", "app_writer1", "svc_orders", "abc"} {
		if err := ValidateMongoUsername(name); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}
	refused := []string{
		"", "ab", "Reporting", "1abc", "_abc", "a-b", "a b", "a.b", `a"b`, "a';drop role x;--",
		strings.Repeat("a", 64),
		"postgres", "app", "excalibase_app", "auth_admin", "cdc_watcher", "documentdb", "streaming_replica",
		"public", "anon", "authenticated", "service_role", "authenticator", "admin", "root",
		"pg_monitor", "pg_anything", "documentdb_admin_role", "documentdb_x", "excalibase_x", "cnpg_pooler_pgbouncer",
	}
	for _, name := range refused {
		if err := ValidateMongoUsername(name); !errors.Is(err, ErrInvalidMongoUsername) {
			t.Errorf("%q: got %v, want ErrInvalidMongoUsername", name, err)
		}
	}
}

func TestOnlyTheTwoAgreedRolesAreAccepted(t *testing.T) {
	for _, role := range []MongoUserRole{MongoRoleReadWrite, MongoRoleRead} {
		if err := role.Validate(); err != nil {
			t.Errorf("%q refused: %v", role, err)
		}
	}
	for _, role := range []MongoUserRole{"", "admin", "readWriteAnyDatabase", "clusterAdmin", "READ"} {
		if err := role.Validate(); !errors.Is(err, ErrInvalidMongoUserRole) {
			t.Errorf("%q: got %v, want ErrInvalidMongoUserRole", role, err)
		}
	}
}

func TestCreateMongoUserReturnsThePasswordOnceAndFilesItInVault(t *testing.T) {
	h := newMongoUsersHarness(t)

	cred, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead)
	if err != nil {
		t.Fatalf("CreateMongoUser: %v", err)
	}
	if cred.Username != "reporting" || cred.Role != MongoRoleRead || len(cred.Password) < 32 {
		t.Fatalf("credential: %+v", cred)
	}
	record := h.vault.data["projects/"+mongoProject+"/mongo-users/reporting"]
	if record["password"] != cred.Password || record["role"] != string(MongoRoleRead) || record["username"] != "reporting" {
		t.Errorf("vault record: %v", record)
	}
}

func TestCreateMongoUserSendsAVerifierNeverThePassword(t *testing.T) {
	h := newMongoUsersHarness(t)

	cred, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead)
	if err != nil {
		t.Fatalf("CreateMongoUser: %v", err)
	}
	for _, argv := range h.kube.ExecCommands {
		if strings.Contains(argv, "reporting") || strings.Contains(argv, "SCRAM") {
			t.Errorf("name or verifier in exec argv (audited): %s", argv)
		}
	}
	literals := strings.Join(decodedLiterals(h.stdinSQL()), " ")
	if strings.Contains(h.stdinSQL(), cred.Password) || strings.Contains(literals, cred.Password) {
		t.Fatal("the plaintext password reached the database")
	}
	if !strings.Contains(literals, "SCRAM-SHA-256$4096:") {
		t.Errorf("no SCRAM verifier was set: %q", literals)
	}
}

func TestCreateMongoUserRunsOnTheCurrentPrimary(t *testing.T) {
	h := newMongoUsersHarness(t)
	if _, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatalf("CreateMongoUser: %v", err)
	}
	if !containsCall(h.kube.Calls, "ExecInPod:"+mongoPrimary) {
		t.Errorf("not executed on the primary: %v", h.kube.Calls)
	}
}

func TestReadWriteAndReadOnlyUsersGetTheirDocumentDBRoles(t *testing.T) {
	cases := map[MongoUserRole][]string{
		MongoRoleReadWrite: {"documentdb_admin_role", "excalibase_mongo_users"},
		MongoRoleRead:      {"documentdb_readonly_role", "pg_read_all_data", "excalibase_mongo_users"},
	}
	for role, grants := range cases {
		sql := createdUserSQL(t, role)
		expectAll(t, string(role), sql, grants)
		if role == MongoRoleRead && strings.Contains(sql, "documentdb_admin_role") {
			t.Errorf("a read-only user was granted the admin role: %q", sql)
		}
		expectNone(t, string(role), sql, []string{"SUPERUSER", "CREATEROLE", "CREATEDB", "REPLICATION", "BYPASSRLS", "ADMIN OPTION"})
	}
}

func createdUserSQL(t *testing.T, role MongoUserRole) string {
	t.Helper()
	h := newMongoUsersHarness(t)
	if _, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "svc_user", role); err != nil {
		t.Fatalf("CreateMongoUser %s: %v", role, err)
	}
	return h.stdinSQL()
}

func expectAll(t *testing.T, what, sql string, fragments []string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(sql, fragment) {
			t.Errorf("%s: missing %s in %q", what, fragment, sql)
		}
	}
}

func expectNone(t *testing.T, what, sql string, fragments []string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(sql, fragment) {
			t.Errorf("%s: statement carries %s: %q", what, fragment, sql)
		}
	}
}

func TestCreateMongoUserRefusesANameTheProjectAlreadyHas(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleReadWrite); !errors.Is(err, ErrMongoUserExists) {
		t.Fatalf("duplicate: got %v, want ErrMongoUserExists", err)
	}
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, mongoOwner, MongoRoleRead); !errors.Is(err, ErrMongoUserExists) {
		t.Fatalf("owner name: got %v, want ErrMongoUserExists", err)
	}
}

func TestCreateMongoUserIsCappedPerProject(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	if MaxMongoUsersPerProject != 100 {
		t.Fatalf("cap is %d; the owner set 100", MaxMongoUsersPerProject)
	}
	for i := range MaxMongoUsersPerProject {
		if _, err := h.svc.CreateMongoUser(ctx, mongoProject, fmt.Sprintf("user_%02d", i), MongoRoleRead); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	execs := len(h.kube.ExecStdin)
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "one_too_many", MongoRoleRead); !errors.Is(err, ErrMongoUserLimit) {
		t.Fatalf("over the cap: got %v, want ErrMongoUserLimit", err)
	}
	if len(h.kube.ExecStdin) != execs {
		t.Error("the database was touched for a refused user")
	}
}

func TestMongoUsersNeedADocumentDBProject(t *testing.T) {
	h := newMongoUsersHarness(t)
	inst, _ := h.store.FindByProjectID(mongoProject)
	inst.DocumentDB = false
	if err := h.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); !errors.Is(err, ErrNotDocumentDBProject) {
		t.Errorf("create: got %v", err)
	}
	if _, err := h.svc.ListMongoUsers(ctx, mongoProject); !errors.Is(err, ErrNotDocumentDBProject) {
		t.Errorf("list: got %v", err)
	}
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "reporting"); !errors.Is(err, ErrNotDocumentDBProject) {
		t.Errorf("rotate: got %v", err)
	}
	if len(h.kube.ExecStdin) != 0 {
		t.Error("SQL ran for a project without DocumentDB")
	}
}

func TestMongoUserChangesNeedAnActiveProject(t *testing.T) {
	h := newMongoUsersHarness(t)
	inst, _ := h.store.FindByProjectID(mongoProject)
	inst.Status = string(domain.StatusPaused)
	if err := h.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead); !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("paused project: got %v, want ErrProjectNotActive", err)
	}
}

func TestAFailedCreateLeavesNoRecord(t *testing.T) {
	h := newMongoUsersHarness(t)
	h.kube.WildcardExecError = errors.New("role already exists")
	if _, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead); err == nil {
		t.Fatal("a refused CREATE ROLE was reported as success")
	}
	if len(h.vault.data) != 0 {
		t.Errorf("vault kept a record for a user that does not exist: %v", h.vault.data)
	}
}

type failingPutVault struct{ *fakeVault }

func (f failingPutVault) Put(string, map[string]string) error { return errors.New("vault down") }

func TestAVaultFailureDropsTheRoleItJustCreated(t *testing.T) {
	h := newMongoUsersHarness(t)
	h.svc.SetVault(failingPutVault{h.vault})
	if _, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead); err == nil {
		t.Fatal("an unrecorded user was reported as created")
	}
	last := h.kube.ExecStdin[len(h.kube.ExecStdin)-1]
	if !strings.Contains(last, "DROP ROLE") || !strings.Contains(strings.Join(decodedLiterals(last), " "), "reporting") {
		t.Errorf("the created role was not dropped: %q", last)
	}
}

func TestListMongoUsersNeverCarriesAPassword(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	for _, name := range []string{"writer", "reader"} {
		role := MongoRoleRead
		if name == "writer" {
			role = MongoRoleReadWrite
		}
		if _, err := h.svc.CreateMongoUser(ctx, mongoProject, name, role); err != nil {
			t.Fatal(err)
		}
	}
	users, err := h.svc.ListMongoUsers(ctx, mongoProject)
	if err != nil {
		t.Fatalf("ListMongoUsers: %v", err)
	}
	if len(users) != 2 || users[0].Username != "reader" || users[0].Role != MongoRoleRead ||
		users[1].Username != "writer" || users[1].Role != MongoRoleReadWrite {
		t.Fatalf("users: %+v", users)
	}
	if users[0].CreatedAt == "" {
		t.Error("no creation time")
	}
}

func TestRotateMongoUserSetsANewPassword(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	first, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := h.svc.RotateMongoUser(ctx, mongoProject, "reporting")
	if err != nil {
		t.Fatalf("RotateMongoUser: %v", err)
	}
	if rotated.Password == first.Password || rotated.Role != MongoRoleRead {
		t.Fatalf("rotation: %+v", rotated)
	}
	last := h.kube.ExecStdin[len(h.kube.ExecStdin)-1]
	if !strings.Contains(last, "ALTER ROLE") || strings.Contains(last, rotated.Password) {
		t.Errorf("rotation statement: %q", last)
	}
	if h.vault.data["projects/"+mongoProject+"/mongo-users/reporting"]["password"] != rotated.Password {
		t.Error("vault still holds the old password")
	}
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "nobody"); !errors.Is(err, ErrMongoUserNotFound) {
		t.Errorf("unknown user: got %v", err)
	}
}

func TestDeleteMongoUserDropsTheRoleAndItsRecord(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "reporting"); err != nil {
		t.Fatalf("DeleteMongoUser: %v", err)
	}
	last := h.kube.ExecStdin[len(h.kube.ExecStdin)-1]
	for _, want := range []string{"pg_terminate_backend", "REASSIGN OWNED", "DROP OWNED", "DROP ROLE"} {
		if !strings.Contains(last, want) {
			t.Errorf("delete statement is missing %s: %q", want, last)
		}
	}
	if _, ok := h.vault.data["projects/"+mongoProject+"/mongo-users/reporting"]; ok {
		t.Error("vault still holds the deleted user")
	}
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "reporting"); !errors.Is(err, ErrMongoUserNotFound) {
		t.Errorf("second delete: got %v", err)
	}
}

// Only a member of the Mongo users group can be dropped through this path,
// so a name that slipped past validation can never take a platform role.
func TestDeleteOnlyDropsMembersOfTheMongoUsersGroup(t *testing.T) {
	sql := dropMongoUserSQL("reporting")
	if !strings.Contains(sql, "excalibase_mongo_users") || !strings.Contains(sql, "pg_auth_members") ||
		!strings.Contains(sql, "NOT u.rolsuper") {
		t.Errorf("drop is not confined to the group: %q", sql)
	}
}

// A project comes into existence with no Mongo users of its own. A restored
// cluster carries the source's, with the source's passwords, so registration
// drops every member of the group before the project is recorded.
func TestRegistrationDropsMongoUsersARestoredClusterBroughtWithIt(t *testing.T) {
	kube := k8s.NewMockClient()
	svc := documentDBService(t, kube)
	if err := svc.enableDocumentDB(context.Background(), documentDBProject(), idleContext()); err != nil {
		t.Fatalf("enableDocumentDB: %v", err)
	}
	dropped := execCommandsMentioning(kube, "DROP ROLE")
	if len(dropped) != 1 || !strings.Contains(dropped[0], "excalibase_mongo_users") || !strings.Contains(dropped[0], "-d postgres") {
		t.Fatalf("restored Mongo users are not dropped: %v", kube.ExecCommands)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

// DocumentDB is offered on 15 to 17, and a per-user peer line works on all.
func TestMongoUsersWorkOnPostgres15(t *testing.T) {
	h := newMongoUsersHarness(t)
	inst, _ := h.store.FindByProjectID(mongoProject)
	inst.PostgresVersion = "15"
	if err := h.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatalf("postgres 15: %v", err)
	}
	if !slices.Contains(h.peerLines(t), "local postgres reporting") {
		t.Errorf("no peer line: %q", h.peerLines(t))
	}
}

// DocumentDB connects back over the socket as the user; without its peer
// line every Mongo command fails. The line goes in with the user, the create
// waits until the primary has loaded it, and it leaves with the user.
func TestAMongoUsersPeerLineComesAndGoesWithIt(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.peerLines(t), "local postgres reporting") {
		t.Fatalf("created without a peer line: %q", h.peerLines(t))
	}
	if !strings.Contains(h.stdinSQL(), "pg_ident_file_mappings") || !strings.Contains(h.stdinSQL(), "pg_reload_conf()") {
		t.Error("the create did not wait for the primary to load the line")
	}
	if !slices.Contains(h.peerLines(t), "local postgres "+mongoOwner) {
		t.Error("a platform peer line was lost")
	}
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "reporting"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.peerLines(t), "local postgres reporting") {
		t.Errorf("peer line left behind: %q", h.peerLines(t))
	}
}

func TestAPeerLineThatNeverLoadsUndoesTheCreate(t *testing.T) {
	h := newMongoUsersHarness(t)
	h.kube.ExecOutput[mongoPrimary] = "f\n"
	if _, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead); err == nil {
		t.Fatal("a user whose peer line never loaded was reported as created")
	}
	expectUndone(t, h)
}

func TestAClusterThatRefusesThePeerLineUndoesTheCreate(t *testing.T) {
	h := newMongoUsersHarness(t)
	h.kube.UpdateCRDError = errors.New("conflict")
	if _, err := h.svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead); err == nil {
		t.Fatal("reported as created")
	}
	expectUndone(t, h)
}

func expectUndone(t *testing.T, h *mongoUsersHarness) {
	t.Helper()
	last := h.kube.ExecStdin[len(h.kube.ExecStdin)-1]
	if !strings.Contains(last, "DROP ROLE") {
		t.Errorf("the role was not dropped: %q", last)
	}
	if slices.Contains(h.peerLines(t), "local postgres reporting") {
		t.Errorf("peer line left behind: %q", h.peerLines(t))
	}
	if len(h.vault.data) != 0 {
		t.Errorf("vault kept a record: %v", h.vault.data)
	}
}

func TestADeleteWhosePeerLineCannotBeRemovedKeepsTheRecordForARetry(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatal(err)
	}
	h.kube.UpdateCRDError = errors.New("conflict")
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "reporting"); err == nil {
		t.Fatal("reported as deleted")
	}
	if _, ok := h.vault.data["projects/"+mongoProject+"/mongo-users/reporting"]; !ok {
		t.Error("record gone although the peer line stayed")
	}
	h.kube.UpdateCRDError = nil
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "reporting"); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

type failingListVault struct{ *fakeVault }

func (failingListVault) List(string) ([]string, error) { return nil, errors.New("vault unreachable") }

func TestMongoUsersRefuseWithoutAVaultOrCluster(t *testing.T) {
	h := newMongoUsersHarness(t)
	h.svc.SetVault(&sealedVault{})
	ctx := context.Background()
	if _, err := h.svc.ListMongoUsers(ctx, mongoProject); !errors.Is(err, ErrMongoUsersUnavailable) {
		t.Errorf("sealed list: %v", err)
	}
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); !errors.Is(err, ErrMongoUsersUnavailable) {
		t.Errorf("sealed create: %v", err)
	}
	if _, err := h.svc.ListMongoUsers(ctx, "no-such-project"); !errors.Is(err, ErrMongoUsersUnavailable) {
		t.Errorf("sealed vault answers before the project lookup: %v", err)
	}
	h.svc.SetVault(h.vault)
	if _, err := h.svc.ListMongoUsers(ctx, "no-such-project"); err == nil {
		t.Error("an unknown project listed users")
	}
	noCluster := NewProvisioningService(h.store, provisioner.NewFactory(), nil)
	noCluster.SetVault(h.vault)
	if err := noCluster.DeleteMongoUser(ctx, mongoProject, "reporting"); !errors.Is(err, ErrMongoUsersUnavailable) {
		t.Errorf("no cluster delete: %v", err)
	}
}

func TestAnUnreadableVaultIsAFailureNotAnEmptyProject(t *testing.T) {
	h := newMongoUsersHarness(t)
	h.svc.SetVault(failingListVault{h.vault})
	ctx := context.Background()
	if _, err := h.svc.ListMongoUsers(ctx, mongoProject); err == nil {
		t.Error("list read an unreachable vault as no users")
	}
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); err == nil {
		t.Error("create went ahead without knowing the existing users")
	}
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "reporting"); err == nil || errors.Is(err, ErrMongoUserNotFound) {
		t.Errorf("rotate read an unreachable vault as not found: %v", err)
	}
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "reporting"); err == nil || errors.Is(err, ErrMongoUserNotFound) {
		t.Errorf("delete read an unreachable vault as not found: %v", err)
	}
}

func TestMongoUserChangesNeedAPrimary(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatal(err)
	}
	delete(h.kube.CRDs, mongoNamespace+"/"+mongoProject+"-postgres")
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "other", MongoRoleRead); err == nil {
		t.Error("create without a cluster to read the primary from")
	}
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "reporting"); err == nil {
		t.Error("rotate without a primary")
	}
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "reporting"); err == nil {
		t.Error("delete without a primary")
	}
	h.kube.CRDs[mongoNamespace+"/"+mongoProject+"-postgres"] = &unstructured.Unstructured{Object: map[string]interface{}{"kind": "Cluster"}}
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "reporting"); err == nil {
		t.Error("rotate on a cluster that reports no primary")
	}
}

func TestARefusedStatementLeavesTheRecordAsItWas(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	first, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead)
	if err != nil {
		t.Fatal(err)
	}
	h.kube.WildcardExecError = errors.New("server closed the connection")
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "reporting"); err == nil {
		t.Error("a refused ALTER was reported as a rotation")
	}
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "reporting"); err == nil {
		t.Error("a refused DROP was reported as a delete")
	}
	if h.vault.data["projects/"+mongoProject+"/mongo-users/reporting"]["password"] != first.Password {
		t.Error("the vault record changed although the database did not")
	}
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "exists_db", MongoRoleRead); err == nil {
		t.Error("create reported success")
	}
	h.kube.WildcardExecError = errors.New("ERROR:  role already exists")
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "exists_db", MongoRoleRead); !errors.Is(err, ErrMongoUserExists) {
		t.Errorf("a role the database already has: %v", err)
	}
}

func TestInvalidNamesAreNotFoundForRotateAndDelete(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "Bad;Name"); !errors.Is(err, ErrMongoUserNotFound) {
		t.Errorf("rotate: %v", err)
	}
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "Bad;Name"); !errors.Is(err, ErrMongoUserNotFound) {
		t.Errorf("delete: %v", err)
	}
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "Bad;Name", MongoRoleRead); !errors.Is(err, ErrInvalidMongoUsername) {
		t.Errorf("create: %v", err)
	}
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", "admin"); !errors.Is(err, ErrInvalidMongoUserRole) {
		t.Errorf("role: %v", err)
	}
}

type failingPutAfterFirstVault struct {
	*fakeVault
	puts int
}

func (f *failingPutAfterFirstVault) Put(path string, record map[string]string) error {
	f.puts++
	if f.puts > 1 {
		return errors.New("vault down")
	}
	return f.fakeVault.Put(path, record)
}

func TestARotationTheVaultCouldNotRecordIsReported(t *testing.T) {
	h := newMongoUsersHarness(t)
	h.svc.SetVault(&failingPutAfterFirstVault{fakeVault: h.vault})
	ctx := context.Background()
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "reporting"); err == nil || !strings.Contains(err.Error(), "rotate again") {
		t.Errorf("an unrecorded rotation: %v", err)
	}
}

// flakyVault fails the named operations; everything else is the fake.
type flakyVault struct {
	*fakeVault
	failGet, failDelete bool
}

func (f flakyVault) Get(path string) (map[string]string, error) {
	if f.failGet {
		return nil, errors.New("vault read failed")
	}
	return f.fakeVault.Get(path)
}

func (f flakyVault) Delete(path string) error {
	if f.failDelete {
		return errors.New("vault delete failed")
	}
	return f.fakeVault.Delete(path)
}

func TestVaultReadAndDeleteFailuresAreReported(t *testing.T) {
	h := newMongoUsersHarness(t)
	ctx := context.Background()
	if _, err := h.svc.CreateMongoUser(ctx, mongoProject, "reporting", MongoRoleRead); err != nil {
		t.Fatal(err)
	}
	// A nested path is not a user of this project.
	h.vault.data["projects/"+mongoProject+"/mongo-users/reporting/extra"] = map[string]string{}
	users, err := h.svc.ListMongoUsers(ctx, mongoProject)
	if err != nil || len(users) != 1 {
		t.Fatalf("list with a nested path: %v %+v", err, users)
	}
	h.svc.SetVault(flakyVault{fakeVault: h.vault, failGet: true})
	if _, err := h.svc.ListMongoUsers(ctx, mongoProject); err == nil {
		t.Error("list read an unreadable record as nothing")
	}
	if _, err := h.svc.RotateMongoUser(ctx, mongoProject, "reporting"); err == nil {
		t.Error("rotate went ahead without its record")
	}
	h.svc.SetVault(flakyVault{fakeVault: h.vault, failDelete: true})
	if err := h.svc.DeleteMongoUser(ctx, mongoProject, "reporting"); err == nil {
		t.Error("a delete whose record stayed was reported done")
	}
}

// secondExecFails lets the CREATE through and refuses the compensating DROP.
type secondExecFails struct {
	*k8s.MockClient
	calls int
}

func (s *secondExecFails) ExecInPodStdin(ctx context.Context, namespace, pod, container string, cmd []string, stdin string) (string, error) {
	s.calls++
	if s.calls > 1 {
		return "", errors.New("pod gone")
	}
	return s.MockClient.ExecInPodStdin(ctx, namespace, pod, container, cmd, stdin)
}

func TestAFailedCompensationStillFailsTheCreate(t *testing.T) {
	h := newMongoUsersHarness(t)
	svc := NewProvisioningService(h.store, provisioner.NewFactory(), &secondExecFails{MockClient: h.kube})
	svc.mongoIdentWait = h.svc.mongoIdentWait
	svc.SetVault(failingPutVault{h.vault})
	if _, err := svc.CreateMongoUser(context.Background(), mongoProject, "reporting", MongoRoleRead); err == nil {
		t.Fatal("an unrecorded user was reported as created")
	}
}
