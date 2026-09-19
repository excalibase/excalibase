package service

import (
	"context"
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/natsauth"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/lib/pq"
)

const (
	testApplyManifestPrefix = "ApplyManifestURL:"
	testAlterPub            = "ALTER PUBLICATION"
)

// --- OperatorSetupService ---
//
// Pure-mock coverage. The mock GetDeployment returns true unconditionally,
// so IsOperatorInstalled lights up the happy paths. waitForOperator
// returns immediately when the operator looks installed, which keeps these
// tests fast (no fake clock needed).

func TestOperatorSetup_InstallOperator_PostgreSQL(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	if err := svc.InstallOperator(context.Background(), domain.PostgreSQL); err != nil {
		t.Fatalf("InstallOperator: %v", err)
	}
	// ApplyManifestURL was recorded — proves the URL lookup table was hit.
	var sawApply bool
	for _, c := range mock.Calls {
		if len(c) >= len(testApplyManifestPrefix) && c[:len(testApplyManifestPrefix)] == testApplyManifestPrefix {
			sawApply = true
			break
		}
	}
	if !sawApply {
		t.Errorf("expected ApplyManifestURL call, got %v", mock.Calls)
	}
}

func TestOperatorSetup_InstallOperator_UnsupportedType(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	err := svc.InstallOperator(context.Background(), domain.DatabaseType("not-a-real-type"))
	if err == nil {
		t.Error("expected error for unsupported db type")
	}
}

func TestOperatorSetup_IsOperatorInstalled_AllTypes(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)
	ctx := context.Background()

	// Mock returns true for every deployment lookup, so each known type reports installed.
	if !svc.IsOperatorInstalled(ctx, domain.PostgreSQL) {
		t.Error("postgres should be reported installed by mock")
	}
	if !svc.IsOperatorInstalled(ctx, domain.MySQL) {
		t.Error("mysql should be reported installed by mock")
	}
	if !svc.IsOperatorInstalled(ctx, domain.MongoDB) {
		t.Error("mongo should be reported installed by mock")
	}
	// Unknown type → false (no entry in operatorDeployments map)
	if svc.IsOperatorInstalled(ctx, domain.DatabaseType("nope")) {
		t.Error("unknown type should not be reported installed")
	}
}

func TestOperatorSetup_GetStatus(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	status := svc.GetStatus(context.Background())
	if !status.PostgreSQL || !status.MySQL || !status.MongoDB {
		t.Errorf("all installed under mock, got %+v", status)
	}
}

// --- PgDogNotifier ---
//
// Empty natsURL → no NATS connection, all publish() calls become no-ops.
// We can drive RegisterCluster/DeregisterCluster against a fake store and
// verify both happy path and the early-return when store==nil.

type fakePgDogStore struct {
	databases            []domain.PgDogDatabase
	users                []domain.PgDogUser
	removedDatabases     []string
	removedUserDatabases []string
	registerErr          error
	removeErr            error
}

func (f *fakePgDogStore) RegisterPgDogDatabase(_ context.Context, d *domain.PgDogDatabase) error {
	if f.registerErr != nil {
		return f.registerErr
	}
	f.databases = append(f.databases, *d)
	return nil
}

func (f *fakePgDogStore) RemovePgDogDatabase(_ context.Context, name string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removedDatabases = append(f.removedDatabases, name)
	return nil
}

func (f *fakePgDogStore) RegisterPgDogUser(_ context.Context, u *domain.PgDogUser) error {
	if f.registerErr != nil {
		return f.registerErr
	}
	f.users = append(f.users, *u)
	return nil
}

func (f *fakePgDogStore) RemovePgDogUsers(_ context.Context, database string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removedUserDatabases = append(f.removedUserDatabases, database)
	return nil
}

var testPgDogAppRole = []PgDogRole{{Name: "excalibase_app", Password: "p"}}

func TestPgDogNotifier_NewWithEmptyURL(t *testing.T) {
	n, err := NewPgDogNotifier(nil, "")
	if err != nil {
		t.Fatalf("expected no error for empty natsURL, got %v", err)
	}
	if n == nil {
		t.Fatal("notifier should be non-nil even with no NATS")
	}
	// Close on a notifier without a NATS conn must be safe.
	n.Close()
}

func TestPgDogNotifier_NewWithUnreachableBusKeepsRetrying(t *testing.T) {
	// With the shared dial options an unreachable bus is a state, not a
	// construction failure: the notifier comes up disconnected and keeps
	// retrying, instead of taking the control plane down with it.
	opts, err := natsauth.ClientOptions(natsauth.PrincipalProvisioning, "", "CDC")
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	n, err := NewPgDogNotifier(nil, "nats://127.0.0.1:1", opts...)
	if err != nil {
		t.Fatalf("construction failed against an unreachable bus: %v", err)
	}
	defer n.Close()
	if n.Connected() {
		t.Error("notifier reports connected against an unreachable bus")
	}
}

func TestPgDogNotifier_NewWithMalformedURLStillFails(t *testing.T) {
	// A URL that cannot be parsed is a configuration error, not a transient
	// outage, and must not be retried silently forever.
	if _, err := NewPgDogNotifier(nil, "://nope"); err == nil {
		t.Error("expected a connect error for a malformed NATS URL")
	}
}

func TestPgDogNotifier_RegisterCluster_NilStore(t *testing.T) {
	n, _ := NewPgDogNotifier(nil, "")
	// store==nil branch returns nil without attempting any work.
	if err := n.RegisterCluster(context.Background(), "p", "ns", "db", testPgDogAppRole); err != nil {
		t.Errorf("expected nil error when store is nil, got %v", err)
	}
}

func TestPgDogNotifier_RegisterCluster_HappyPath(t *testing.T) {
	store := &fakePgDogStore{}
	n, _ := NewPgDogNotifier(store, "")

	roles := []PgDogRole{{Name: "excalibase_app", Password: testutil.FixtureSecret("pgdog-cluster")}}
	if err := n.RegisterCluster(context.Background(), "proj-1", "ns-1", "appdb", roles); err != nil {
		t.Fatalf("RegisterCluster: %v", err)
	}
	// Two databases (primary + replica) and one user must be persisted.
	if len(store.databases) != 2 {
		t.Errorf("expected 2 databases, got %d", len(store.databases))
	}
	if len(store.users) != 1 {
		t.Errorf("expected 1 user, got %d", len(store.users))
	}
	// Primary host should be the -rw service hostname.
	want := "proj-1-postgres-rw.ns-1.svc.cluster.local"
	if store.databases[0].Host != want {
		t.Errorf("primary host: got %q want %q", store.databases[0].Host, want)
	}
}

func TestPgDogNotifier_RegisterCluster_StoreError(t *testing.T) {
	store := &fakePgDogStore{registerErr: errors.New("db down")}
	n, _ := NewPgDogNotifier(store, "")

	err := n.RegisterCluster(context.Background(), "p", "ns", "db", testPgDogAppRole)
	if err == nil {
		t.Error("expected error when store.RegisterPgDogDatabase fails")
	}
}

func TestPgDogNotifier_DeregisterCluster_NilStore(t *testing.T) {
	n, _ := NewPgDogNotifier(nil, "")
	if err := n.DeregisterCluster(context.Background(), "p"); err != nil {
		t.Errorf("expected nil for nil-store path, got %v", err)
	}
}

func TestPgDogNotifier_DeregisterCluster_StoreErrorsAreLogged(t *testing.T) {
	// Deregister logs warnings rather than failing — the operation should
	// still succeed end-to-end so a flaky pgdog table doesn't block deletion.
	store := &fakePgDogStore{removeErr: errors.New("transient")}
	n, _ := NewPgDogNotifier(store, "")

	if err := n.DeregisterCluster(context.Background(), "p"); err != nil {
		t.Errorf("Deregister should swallow store errors, got %v", err)
	}
}

// --- ProvisioningService setters / getters ---
//
// These were 0% because nothing else in service tests actually wires them.
// Hit each setter once and verify the getter reflects the change.

func TestProvisioningService_PublicationName_DefaultAndOverride(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)

	if got := svc.PublicationName(); got != "cdc_watcher_pub" {
		t.Errorf("default publication: got %q, want cdc_watcher_pub", got)
	}

	svc.SetPublicationName("custom_pub")
	if got := svc.PublicationName(); got != "custom_pub" {
		t.Errorf("after Set: got %q, want custom_pub", got)
	}

	// Empty string falls back to the default, mirroring how main.go threads
	// REALTIME_PUBLICATION_NAME (which may be unset).
	svc.SetPublicationName("")
	if got := svc.PublicationName(); got != "cdc_watcher_pub" {
		t.Errorf("empty string should return default, got %q", got)
	}
}

func TestProvisioningService_Setters_NoPanic(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)

	// Each setter is a one-line assignment — call once just to flip
	// the field and prove the method exists / signatures compile.
	svc.SetPgDogNotifier(nil)
	svc.SetOrgStore(nil)
	svc.SetSelfHostedMode(true)
	svc.SetDockerClient(nil)
}

// --- realtime helpers (pure functions, no DB needed) ---

func TestValidIdent(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"valid_name", true},
		{"_underscore_start", true},
		{"camelCase", true},
		{"name_with_123", true},
		{"", false},
		{"1starts_with_digit", false},
		{"has space", false},
		{"has;semicolon", false},
		{"has\"quote", false},
		{"unicode_ñ", false},
	}
	for _, c := range cases {
		if got := validIdent(c.in); got != c.ok {
			t.Errorf("validIdent(%q) = %v, want %v", c.in, got, c.ok)
		}
	}
	// Length boundary: exactly 63 chars is OK, 64 is not.
	exactly63 := make([]byte, 63)
	for i := range exactly63 {
		exactly63[i] = 'a'
	}
	if !validIdent(string(exactly63)) {
		t.Error("63-char identifier should be valid")
	}
	tooLong := exactly63
	tooLong = append(tooLong, 'a')
	if validIdent(string(tooLong)) {
		t.Error("64-char identifier should be invalid")
	}
}

func TestIsPgCode(t *testing.T) {
	pqErr := &pq.Error{Code: "42710"}
	if !isPgCode(pqErr, "42710") {
		t.Error("direct match should be true")
	}
	if isPgCode(pqErr, "42704") {
		t.Error("different code should be false")
	}
	// Wrapped pq.Error is detected via Unwrap().
	wrapped := wrapErr{inner: pqErr}
	if !isPgCode(wrapped, "42710") {
		t.Error("wrapped pq.Error should still match by code")
	}
	// Non-pq error returns false without panicking.
	if isPgCode(errors.New("plain"), "42710") {
		t.Error("non-pq error should not match any code")
	}
	if isPgCode(nil, "42710") {
		t.Error("nil error should not match")
	}
}

type wrapErr struct{ inner error }

func (w wrapErr) Error() string { return w.inner.Error() }
func (w wrapErr) Unwrap() error { return w.inner }

// --- realtime constructors ---

func TestNewRealtimeService_DefaultPubName(t *testing.T) {
	svc := NewRealtimeService(nil)
	if svc.publicationName != DefaultPublicationName {
		t.Errorf("publicationName: got %q, want %q", svc.publicationName, DefaultPublicationName)
	}
}

func TestNewRealtimeServiceWithName_EmptyFallsBack(t *testing.T) {
	svc := NewRealtimeServiceWithName(nil, "")
	if svc.publicationName != DefaultPublicationName {
		t.Errorf("empty name should fall back to default, got %q", svc.publicationName)
	}

	svc2 := NewRealtimeServiceWithName(nil, "my_pub")
	if svc2.publicationName != "my_pub" {
		t.Errorf("explicit name not honoured, got %q", svc2.publicationName)
	}
}

// --- helpers in provisioning.go ---

func TestBoolPtrIntPtr(t *testing.T) {
	bp := boolPtr(true)
	if bp == nil || *bp != true {
		t.Errorf("boolPtr(true): got %v", bp)
	}
	ip := intPtr(42)
	if ip == nil || *ip != 42 {
		t.Errorf("intPtr(42): got %v", ip)
	}
}

// --- ProvisioningService.ProvisionBYOC ---
//
// BYOC short-circuits the K8s pipeline: it just stores credentials and
// records the instance. No vault, no orgStore — both are optional.
// Covers the happy-path branch and the unique-ref retry guard.

func TestProvisionBYOC_HappyPath_NoVaultNoOrgStore(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)

	resp, err := svc.ProvisionBYOC(context.Background(), domain.BYOCRequest{
		ProjectName: "external-pg",
		OrgID:       "org-x",
		Host:        "db.example.com",
		Port:        5432,
		Database:    "appdb",
		Username:    "ext",
		Password:    testutil.FixturePassword("byoc-ext"),
	})
	if err != nil {
		t.Fatalf("ProvisionBYOC: %v", err)
	}
	if resp.Status != "ACTIVE" {
		t.Errorf("status: got %s, want ACTIVE", resp.Status)
	}
	if resp.CurrentStage != domain.StageCompleted {
		t.Errorf("stage: got %s, want COMPLETED", resp.CurrentStage)
	}
	if resp.Host != "db.example.com" {
		t.Errorf("host: got %s, want db.example.com", resp.Host)
	}

	// Persisted instance reflects the BYOC mode + display name.
	got, err := store.FindByProjectID(resp.ProjectID)
	if err != nil || got == nil {
		t.Fatalf("FindByProjectID: %v / %v", err, got)
	}
	if got.DeploymentMode != domain.ModeBYOC {
		t.Errorf("deploymentMode: got %s, want BYOC", got.DeploymentMode)
	}
	if got.ProjectName != "external-pg" {
		t.Errorf("projectName preserved as display name: got %s", got.ProjectName)
	}
}

// --- BackupService.GetInstance ---

func TestBackupService_GetInstance(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	store.Create(&domain.DatabaseInstance{ProjectID: "bk", Status: "ACTIVE"})

	svc := NewBackupService(store, nil, dir, nil)
	got, err := svc.GetInstance("bk")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got.ProjectID != "bk" {
		t.Errorf("projectID: got %s, want bk", got.ProjectID)
	}

	// Missing project — file-system store returns (nil, nil) for unknown IDs.
	missing, err := svc.GetInstance("missing")
	if err != nil {
		t.Errorf("file-system store treats missing as nil/nil, got err=%v", err)
	}
	if missing != nil {
		t.Errorf("missing project should return nil, got %+v", missing)
	}
}

// --- RealtimeService SQL paths via sqlmock ---
//
// These exercise the SQL templating + scan logic without needing a real
// Postgres container, so they run with the unit suite (no -tags=integration).

func TestRealtimeService_ListTables_SQLMock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"schema", "table_name", "enabled"}).
		AddRow("public", "posts", true).
		AddRow("public", "comments", false)
	mock.ExpectQuery("SELECT").
		WithArgs("cdc_watcher_pub").
		WillReturnRows(rows)

	svc := NewRealtimeService(db)
	got, err := svc.ListTables(context.Background())
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(got) != 2 || got[0].Table != "posts" || !got[0].Enabled {
		t.Errorf("unexpected rows: %+v", got)
	}
}

func TestRealtimeService_ListTables_QueryError(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("db down"))

	svc := NewRealtimeService(db)
	if _, err := svc.ListTables(context.Background()); err == nil {
		t.Error("expected wrapped query error")
	}
}

func TestRealtimeService_EnableTable_HappyPath(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectExec(regexp.QuoteMeta(`ALTER PUBLICATION "cdc_watcher_pub" ADD TABLE "public"."posts"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	svc := NewRealtimeService(db)
	if err := svc.EnableTable(context.Background(), "public", "posts"); err != nil {
		t.Fatalf("EnableTable: %v", err)
	}
}

func TestRealtimeService_EnableTable_RejectsBadIdent(t *testing.T) {
	db, _, _ := sqlmock.New()
	defer db.Close()
	svc := NewRealtimeService(db)

	// Bad schema (contains space) must short-circuit before any SQL is sent.
	if err := svc.EnableTable(context.Background(), "bad name", "posts"); err == nil {
		t.Error("expected validation error for bad schema")
	}
}

func TestRealtimeService_EnableTable_SwallowsAlreadyMember(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	// Postgres returns 42710 when a table is already in the publication.
	// EnableTable must treat that as success.
	mock.ExpectExec(testAlterPub).
		WillReturnError(&pq.Error{Code: "42710"})

	svc := NewRealtimeService(db)
	if err := svc.EnableTable(context.Background(), "public", "posts"); err != nil {
		t.Errorf("42710 should be swallowed, got %v", err)
	}
}

func TestRealtimeService_DisableTable_SwallowsNotMember(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	// 42704 = undefined object (not in publication). Idempotent disable.
	mock.ExpectExec(testAlterPub).
		WillReturnError(&pq.Error{Code: "42704"})

	svc := NewRealtimeService(db)
	if err := svc.DisableTable(context.Background(), "public", "posts"); err != nil {
		t.Errorf("42704 should be swallowed, got %v", err)
	}
}

func TestRealtimeService_DisableTable_PropagatesOtherErrors(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectExec(testAlterPub).
		WillReturnError(errors.New("connection lost"))

	svc := NewRealtimeService(db)
	if err := svc.DisableTable(context.Background(), "public", "posts"); err == nil {
		t.Error("non-pg error should not be swallowed")
	}
}

func TestRealtimeService_DisableTable_RejectsBadIdent(t *testing.T) {
	db, _, _ := sqlmock.New()
	defer db.Close()
	svc := NewRealtimeService(db)
	if err := svc.DisableTable(context.Background(), "public", "bad;ident"); err == nil {
		t.Error("expected validation error for bad table")
	}
}

func TestRealtimeService_EnableAll_AddsDisabledTables(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()

	rows := sqlmock.NewRows([]string{"schema", "table_name", "enabled"}).
		AddRow("public", "posts", false).
		AddRow("public", "comments", true)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)
	// Only the disabled row should appear in the bulk ADD.
	mock.ExpectExec(regexp.QuoteMeta(`ALTER PUBLICATION "cdc_watcher_pub" ADD TABLE "public"."posts"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	svc := NewRealtimeService(db)
	added, err := svc.EnableAll(context.Background())
	if err != nil {
		t.Fatalf("EnableAll: %v", err)
	}
	if added != 1 {
		t.Errorf("added: got %d, want 1", added)
	}
}

func TestRealtimeService_EnableAll_NoOpWhenAllEnabled(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	rows := sqlmock.NewRows([]string{"schema", "table_name", "enabled"}).
		AddRow("public", "posts", true)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)
	// No ALTER expected — early-return path.

	svc := NewRealtimeService(db)
	n, err := svc.EnableAll(context.Background())
	if err != nil || n != 0 {
		t.Errorf("EnableAll: n=%d err=%v, want 0/nil", n, err)
	}
}

func TestRealtimeService_DisableAll_DropsEnabledTables(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()

	rows := sqlmock.NewRows([]string{"schema", "table_name", "enabled"}).
		AddRow("public", "posts", true).
		AddRow("public", "comments", false)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta(`ALTER PUBLICATION "cdc_watcher_pub" DROP TABLE "public"."posts"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	svc := NewRealtimeService(db)
	dropped, err := svc.DisableAll(context.Background())
	if err != nil {
		t.Fatalf("DisableAll: %v", err)
	}
	if dropped != 1 {
		t.Errorf("dropped: got %d, want 1", dropped)
	}
}

func TestRealtimeService_DisableAll_NoOpWhenNoneEnabled(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	rows := sqlmock.NewRows([]string{"schema", "table_name", "enabled"}).
		AddRow("public", "posts", false)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)

	svc := NewRealtimeService(db)
	n, err := svc.DisableAll(context.Background())
	if err != nil || n != 0 {
		t.Errorf("DisableAll: n=%d err=%v, want 0/nil", n, err)
	}
}

func TestRealtimeService_BulkOps_PropagateListError(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("boom"))
	svc := NewRealtimeService(db)
	if _, err := svc.EnableAll(context.Background()); err == nil {
		t.Error("EnableAll should propagate ListTables error")
	}

	mock.ExpectQuery("SELECT").WillReturnError(errors.New("boom"))
	if _, err := svc.DisableAll(context.Background()); err == nil {
		t.Error("DisableAll should propagate ListTables error")
	}
}
