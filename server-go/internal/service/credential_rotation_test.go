package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

const (
	rotProject   = "rot-db"
	rotNamespace = "org1-rot-db"
	rotPod       = "org1-rot-db/rot-db-postgres-1"
	rotOwner     = "rot_owner"

	rotAdminPending = "projects/rot-db/credentials/pending/admin"
	rotAdminCurrent = "projects/rot-db/credentials/admin"
	rotAuthCurrent  = "projects/rot-db/credentials/auth_admin"
	rotAppCurrent   = "projects/rot-db/credentials/excalibase_app"
	rotPendingPart  = "/credentials/pending/"
)

// fakeRoleVerifier stands in for the connection that proves a password opens
// the project's database. accept answers the way postgres would.
type fakeRoleVerifier struct {
	accept func(username, password string) error
	calls  int
}

func (f *fakeRoleVerifier) VerifyRole(_ context.Context, _ *domain.DatabaseInstance, username, password string) error {
	f.calls++
	if f.accept == nil {
		return nil
	}
	return f.accept(username, password)
}

type rotationHarness struct {
	svc      *ProvisioningService
	store    *storage.FileSystemStore
	failing  *failingStore
	vault    *fakeVault
	kube     *k8s.MockClient
	verifier *fakeRoleVerifier
	events   *fakePolicyPublisher
}

func newRotationHarness(t *testing.T) *rotationHarness {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	port := 5432
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: rotProject, OrgID: "org1", Namespace: rotNamespace,
		DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE",
		Host: "h.local", Port: &port, DatabaseName: "app",
		Username: rotOwner, Password: testutil.FixturePassword(rotProject), SSLMode: "require",
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	h := &rotationHarness{
		store:    store,
		failing:  &failingStore{InstanceStore: store},
		vault:    newFakeVault(),
		kube:     k8s.NewMockClient(),
		verifier: &fakeRoleVerifier{},
		events:   &fakePolicyPublisher{},
	}
	h.kube.ExecOutput[rotPod] = "ALTER ROLE"
	for path, username := range map[string]string{
		rotAdminCurrent: rotOwner,
		rotAuthCurrent:  roleAuthAdmin,
		rotAppCurrent:   roleApp,
	} {
		h.vault.data[path] = map[string]string{
			"host": "h.local", "port": "5432", "database": "app",
			"username": username, "password": testutil.FixturePassword(username),
		}
	}

	h.svc = NewProvisioningService(h.failing, provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(h.kube, "")), h.kube)
	h.svc.SetVault(h.vault)
	h.svc.SetCredentialVerifier(h.verifier)
	h.svc.SetProjectEventPublisher(h.events)
	return h
}

// pendingPaths lists every pending credential the vault still holds for the
// project, so a test can assert there is no residue.
func (h *rotationHarness) pendingPaths(t *testing.T) []string {
	t.Helper()
	all, err := h.vault.List(vaultCredentialPrefix(rotProject))
	if err != nil {
		t.Fatalf("list credentials: %v", err)
	}
	var pending []string
	for _, p := range all {
		if strings.Contains(p, rotPendingPart) {
			pending = append(pending, p)
		}
	}
	return pending
}

func (h *rotationHarness) storedPassword(t *testing.T) string {
	t.Helper()
	inst, err := h.store.FindByProjectID(rotProject)
	if err != nil || inst == nil {
		t.Fatalf("read project row: %v", err)
	}
	return inst.Password
}

// A rotation whose ALTER reached the database but whose persist failed must
// leave the new password recoverable. The database now wants a value the row
// does not hold, so the only thing standing between the project and an
// operator-run forced rotation is the pending record — and a retry must
// converge on exactly that value rather than minting a third one.
func TestRotateCredentialsLeavesPendingRecoverableWhenPersistFails(t *testing.T) {
	h := newRotationHarness(t)
	h.failing.updateErr = errors.New("platform database unavailable")

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err == nil {
		t.Fatal("rotation must report the failed persist")
	}

	pending := h.pendingPaths(t)
	if len(pending) != 1 || pending[0] != rotAdminPending {
		t.Fatalf("expected exactly the owner pending credential, got %v", pending)
	}
	recorded := h.vault.data[rotAdminPending]["password"]
	if recorded == "" {
		t.Fatal("the pending credential must carry the password the database now wants")
	}
	if h.storedPassword(t) == recorded {
		t.Fatal("the row must not hold the new password when the persist failed")
	}

	h.failing.updateErr = nil
	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("retry must converge: %v", err)
	}
	if got := h.storedPassword(t); got != recorded {
		t.Error("the retry must promote the recorded pending credential, not a fresh one")
	}
	if got := h.vault.data[rotAdminCurrent]["password"]; got != recorded {
		t.Error("the vault copy must hold the promoted credential")
	}
	if left := h.pendingPaths(t); len(left) != 0 {
		t.Errorf("a completed rotation leaves no pending residue, got %v", left)
	}
}

// When the statement never reached the database, the old password is still
// the live one. Keeping a pending value there would tell a later recovery to
// promote a password nothing accepts.
func TestRotateCredentialsRollsBackPendingWhenAlterFails(t *testing.T) {
	h := newRotationHarness(t)
	h.kube.WildcardExecError = errors.New("exec refused")
	h.verifier.accept = func(string, string) error { return errors.New("authentication failed") }

	before := h.storedPassword(t)
	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err == nil {
		t.Fatal("rotation must report the failed statement")
	}
	if got := h.storedPassword(t); got != before {
		t.Error("the old credential must stay the stored one")
	}
	if got := h.vault.data[rotAuthCurrent]["password"]; got != testutil.FixturePassword(roleAuthAdmin) {
		t.Error("the vault copy must be untouched")
	}
	if left := h.pendingPaths(t); len(left) != 0 {
		t.Errorf("a failed statement leaves no pending residue, got %v", left)
	}
}

// psql echoes the statement it could not run, and that statement carries the
// password. Whatever the database says about a failed ALTER, the value must
// not travel out with it.
func TestRotateCredentialsNeverReportsTheNewPassword(t *testing.T) {
	h := newRotationHarness(t)
	var attempted string
	h.kube.ExecError[rotPod] = nil
	h.verifier.accept = func(_, password string) error {
		attempted = password
		return errors.New("authentication failed")
	}
	h.kube.WildcardExecError = errors.New(`ERROR: syntax error at or near "PASSWORD"`)

	_, err := h.svc.RotateCredentials(context.Background(), rotProject)
	if err == nil {
		t.Fatal(testExpectedErr)
	}
	if attempted == "" {
		t.Fatal("the verifier was never given a password to prove")
	}
	if strings.Contains(err.Error(), attempted) {
		t.Error("the reported error carried the new password")
	}
}

// A database that echoes the statement back gets the value scrubbed out.
func TestRedactSecret(t *testing.T) {
	secret := testutil.FixturePassword("redaction")
	got := redactSecret("ALTER USER x PASSWORD '"+secret+"' failed near '"+secret+"'", secret)
	if strings.Contains(got, secret) {
		t.Error("the secret survived redaction")
	}
	if !strings.Contains(got, redactedMarker) {
		t.Errorf("redaction marker missing: %s", got)
	}
	if redactSecret("nothing to redact", "") != "nothing to redact" {
		t.Error("an empty secret must leave the text alone")
	}
}

// The engine and the auth service cache role credentials with a TTL. A
// rotation they are not told about leaves them holding a dead password for
// the rest of that TTL, so the change event is part of the rotation.
func TestRotateCredentialsPublishesCredentialChange(t *testing.T) {
	h := newRotationHarness(t)

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}

	if len(h.events.events) != 1 {
		t.Fatalf("expected one change event, got %d", len(h.events.events))
	}
	evt := h.events.events[0]
	if evt.ProjectID != rotProject || evt.Kind != domain.CredentialChangeKind || evt.Op != "rotate" {
		t.Errorf("unexpected event: project=%s kind=%s op=%s", evt.ProjectID, evt.Kind, evt.Op)
	}
}

// A rotation that did not finish must not tell consumers to refresh: they
// would drop a cached credential that is still the working one.
func TestRotateCredentialsPublishesNothingWhenRotationFails(t *testing.T) {
	h := newRotationHarness(t)
	h.kube.WildcardExecError = errors.New("exec refused")
	h.verifier.accept = func(string, string) error { return errors.New("authentication failed") }

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err == nil {
		t.Fatal(testExpectedErr)
	}
	if len(h.events.events) != 0 {
		t.Errorf("a failed rotation must announce nothing, got %d events", len(h.events.events))
	}
}

// Every role the platform files under the project keeps its vault copy in
// step with the database, owner last — the owner credential is what the API
// hands back, so it is only replaced once the roles behind it are proved.
func TestRotateCredentialsReplacesEveryFiledRoleOwnerLast(t *testing.T) {
	h := newRotationHarness(t)

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}

	for path, username := range map[string]string{
		rotAuthCurrent:  roleAuthAdmin,
		rotAppCurrent:   roleApp,
		rotAdminCurrent: rotOwner,
	} {
		if h.vault.data[path]["password"] == testutil.FixturePassword(username) {
			t.Errorf("%s was not rotated", path)
		}
		if h.vault.data[path]["username"] != username {
			t.Errorf("%s lost its username", path)
		}
	}
	if h.vault.data[rotAdminCurrent]["password"] != h.storedPassword(t) {
		t.Error("the owner's vault copy and the project row must agree")
	}

	order := rotatedRoleOrder(t, h.kube.ExecCommands)
	want := []string{roleAuthAdmin, roleApp, rotOwner}
	if len(order) != len(want) {
		t.Fatalf("expected %d roles altered, got %v", len(want), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("rotation order = %v, want %v", order, want)
		}
	}
}

// rotatedRoleOrder extracts the role each ALTER named, in call order. It
// reads only the quoted identifier — never the rest of the statement, which
// carries the password.
func rotatedRoleOrder(t *testing.T, commands []string) []string {
	t.Helper()
	var roles []string
	for _, cmd := range commands {
		i := strings.Index(cmd, "ALTER USER \"")
		if i < 0 {
			continue
		}
		rest := cmd[i+len("ALTER USER \""):]
		end := strings.Index(rest, "\"")
		if end < 0 {
			t.Fatalf("malformed ALTER statement in call %d", len(roles))
		}
		roles = append(roles, rest[:end])
	}
	return roles
}

// A role the platform never filed has no credential to rotate and no
// consumer holding one. Altering it would fail on a database that does not
// have the role at all.
func TestRotateCredentialsSkipsRolesWithNoFiledCredential(t *testing.T) {
	h := newRotationHarness(t)
	delete(h.vault.data, rotAppCurrent)

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	if order := rotatedRoleOrder(t, h.kube.ExecCommands); len(order) != 2 {
		t.Errorf("only filed roles are rotated, got %v", order)
	}
	if _, ok := h.vault.data[rotAppCurrent]; ok {
		t.Error("a role with no filed credential must not gain one")
	}
}

// Without a way to connect as the rotated role there is no evidence the new
// password works. Assuming it does is exactly the failure this ticket is
// about, so rotation refuses instead.
func TestRotateCredentialsRefusesWithoutAVerifier(t *testing.T) {
	h := newRotationHarness(t)
	h.svc.SetCredentialVerifier(nil)

	_, err := h.svc.RotateCredentials(context.Background(), rotProject)
	if !errors.Is(err, ErrCredentialRotationUnavailable) {
		t.Fatalf("err = %v, want ErrCredentialRotationUnavailable", err)
	}
	if order := rotatedRoleOrder(t, h.kube.ExecCommands); len(order) != 0 {
		t.Errorf("nothing may be altered without a verifier, got %v", order)
	}
}

// Without a vault there is nowhere durable to record the new password before
// the database is changed, which is the whole recovery story.
func TestRotateCredentialsRefusesWithoutAVault(t *testing.T) {
	h := newRotationHarness(t)
	h.svc.SetVault(nil)

	_, err := h.svc.RotateCredentials(context.Background(), rotProject)
	if !errors.Is(err, ErrCredentialRotationUnavailable) {
		t.Fatalf("err = %v, want ErrCredentialRotationUnavailable", err)
	}
}

// A statement that succeeded may well have changed the password even though
// the verification could not confirm it. Deleting the pending record there
// would strand the project, so it stays for a retry.
func TestRotateCredentialsKeepsPendingWhenVerificationFailsAfterASuccessfulAlter(t *testing.T) {
	h := newRotationHarness(t)
	h.verifier.accept = func(username, _ string) error {
		if username == rotOwner {
			return errors.New("connection reset")
		}
		return nil
	}

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err == nil {
		t.Fatal("an unverified credential is an error")
	}
	if left := h.pendingPaths(t); len(left) != 1 || left[0] != rotAdminPending {
		t.Fatalf("the unverified credential must stay recoverable, got %v", left)
	}
	if h.storedPassword(t) != testutil.FixturePassword(rotProject) {
		t.Error("an unverified credential must not be promoted onto the row")
	}
}

// A project not on record cannot be rotated, and nothing may be written
// under its id.
func TestRotateCredentialsUnknownProject(t *testing.T) {
	h := newRotationHarness(t)
	if _, err := h.svc.RotateCredentials(context.Background(), "no-such-project"); err == nil {
		t.Fatal(testExpectedErr)
	}
}

// A vault that cannot be listed cannot say which roles the project has, and
// guessing would either skip a role that needs rotating or ALTER one that
// does not exist. Nothing is touched.
func TestRotateCredentialsRefusesWhenTheVaultCannotBeListed(t *testing.T) {
	h := newRotationHarness(t)
	h.svc.SetVault(&errVault{err: errors.New("vault unreachable")})

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err == nil {
		t.Fatal(testExpectedErr)
	}
	if order := rotatedRoleOrder(t, h.kube.ExecCommands); len(order) != 0 {
		t.Errorf("nothing may be altered against an unreadable vault, got %v", order)
	}
}

// A vault that refuses the pending write has not recorded the new password,
// so the database must not be changed to it.
func TestRotateCredentialsRefusesWhenThePendingRecordCannotBeWritten(t *testing.T) {
	h := newRotationHarness(t)
	h.svc.SetVault(&putRefusingVault{fakeVault: h.vault, refuse: errors.New("vault read-only")})

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err == nil {
		t.Fatal(testExpectedErr)
	}
	if order := rotatedRoleOrder(t, h.kube.ExecCommands); len(order) != 0 {
		t.Errorf("nothing may be altered before the password is recorded, got %v", order)
	}
}

// putRefusingVault records credentials normally until the first Put, which it
// refuses — the vault outage a rotation can hit before it touches anything.
type putRefusingVault struct {
	*fakeVault
	refuse error
}

func (v *putRefusingVault) Put(string, map[string]string) error { return v.refuse }

// A rotation with nobody listening still rotates; the announcement is
// optional wiring, not part of the credential's correctness.
func TestRotateCredentialsWithoutAPublisher(t *testing.T) {
	h := newRotationHarness(t)
	h.svc.SetProjectEventPublisher(nil)

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	if h.vault.data[rotAdminCurrent]["password"] != h.storedPassword(t) {
		t.Error("the owner credential must still be promoted")
	}
}

// The verifier reads the SCHEMA_DB_* overrides the schema and migration
// paths use, so a port-forwarded local database is the one proved.
func TestNewTenantRoleVerifierHonoursConnectionOverrides(t *testing.T) {
	t.Setenv("SCHEMA_DB_HOST", "127.0.0.1")
	t.Setenv("SCHEMA_DB_PORT", "15432")
	t.Setenv("SCHEMA_DB_SSLMODE", "disable")

	v := NewTenantRoleVerifier()
	if v.overrides.host != "127.0.0.1" || v.overrides.port != "15432" || v.overrides.sslmode != "disable" {
		t.Errorf("overrides not read from the environment: %+v", v.overrides)
	}
	if v.timeout != defaultProbeTimeout {
		t.Errorf("timeout = %v, want %v", v.timeout, defaultProbeTimeout)
	}
}

// A credential that does not open the database is an error, and the error
// names the role and nothing else — the connection string it was proved
// with carries the password.
func TestTenantRoleVerifierRejectsACredentialThatCannotConnect(t *testing.T) {
	t.Setenv("SCHEMA_DB_HOST", "127.0.0.1")
	// Port 1 has nothing listening, so the connection cannot succeed.
	t.Setenv("SCHEMA_DB_PORT", "1")
	t.Setenv("SCHEMA_DB_SSLMODE", "disable")
	v := NewTenantRoleVerifier()
	v.timeout = 2 * time.Second

	port := 5432
	inst := &domain.DatabaseInstance{
		ProjectID: rotProject, Host: "h.local", Port: &port,
		DatabaseName: "app", SSLMode: "require",
	}
	secret := testutil.FixturePassword("verifier")
	err := v.VerifyRole(context.Background(), inst, roleApp, secret)
	if err == nil {
		t.Fatal("a credential that cannot connect must not verify")
	}
	if !strings.Contains(err.Error(), roleApp) {
		t.Errorf("the error must name the role: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Error("the error must not carry the password")
	}
}

// With no environment override the project's own SSL mode is the one used:
// verifying over a different mode than the platform connects with would
// prove the wrong thing.
func TestTenantRoleVerifierFallsBackToTheProjectSSLMode(t *testing.T) {
	v := &TenantRoleVerifier{timeout: time.Millisecond}
	port := 5432
	inst := &domain.DatabaseInstance{
		ProjectID: rotProject, Host: "127.0.0.1", Port: &port,
		DatabaseName: "app", SSLMode: "disable",
	}
	if err := v.VerifyRole(context.Background(), inst, roleApp, testutil.FixturePassword("verifier")); err == nil {
		t.Fatal("no database is listening, so this must not verify")
	}
	if v.overrides.sslmode != "" {
		t.Error("the fallback must not be written back onto the verifier")
	}
}
