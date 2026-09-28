package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

const (
	testApplyManifestPrefix = "ApplyManifestURL:"
	testSetRealtime         = "SELECT excalibase.set_realtime_table"
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

// MongoDB-compatible projects are DocumentDB on Postgres; a separate MongoDB
// operator is nothing the platform provisions, so installing one is refused
// before anything is applied to the cluster.
func TestOperatorSetup_InstallOperator_RefusesMongoDB(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	if err := svc.InstallOperator(context.Background(), domain.DatabaseType("MONGODB")); err == nil {
		t.Fatal("expected MONGODB to be refused")
	}
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, testApplyManifestPrefix) {
			t.Errorf("nothing may be applied for MONGODB, got %v", mock.Calls)
		}
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
	// Unknown type → false (no entry in operatorDeployments map)
	if svc.IsOperatorInstalled(ctx, domain.DatabaseType("nope")) {
		t.Error("unknown type should not be reported installed")
	}
}

func TestOperatorSetup_GetStatus(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	status := svc.GetStatus(context.Background())
	if !status.PostgreSQL || !status.MySQL {
		t.Errorf("all installed under mock, got %+v", status)
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
	mock.ExpectExec(regexp.QuoteMeta(`SELECT excalibase.set_realtime_table($1, $2, $3)`)).
		WithArgs("public", "posts", true).
		WillReturnResult(sqlmock.NewResult(0, 0))

	svc := NewRealtimeService(db)
	if err := svc.EnableTable(context.Background(), "public", "posts"); err != nil {
		t.Fatalf("EnableTable: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
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

func TestRealtimeService_EnableTable_PropagatesRefusal(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectExec(testSetRealtime).
		WillReturnError(&pq.Error{Code: "42501", Message: "auth.users cannot be published"})

	svc := NewRealtimeService(db)
	if err := svc.EnableTable(context.Background(), "auth", "users"); err == nil {
		t.Error("a refused table must surface as an error")
	}
}

func TestRealtimeService_DisableTable_HappyPath(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectExec(testSetRealtime).
		WithArgs("public", "posts", false).
		WillReturnResult(sqlmock.NewResult(0, 0))

	svc := NewRealtimeService(db)
	if err := svc.DisableTable(context.Background(), "public", "posts"); err != nil {
		t.Fatalf("DisableTable: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestRealtimeService_DisableTable_PropagatesOtherErrors(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectExec(testSetRealtime).
		WillReturnError(errors.New("connection lost"))

	svc := NewRealtimeService(db)
	if err := svc.DisableTable(context.Background(), "public", "posts"); err == nil {
		t.Error("errors must not be swallowed")
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
	mock.ExpectBegin()
	mock.ExpectExec(testSetRealtime).WithArgs("public", "posts", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	svc := NewRealtimeService(db)
	added, err := svc.EnableAll(context.Background())
	if err != nil {
		t.Fatalf("EnableAll: %v", err)
	}
	if added != 1 {
		t.Errorf("added: got %d, want 1", added)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestRealtimeService_EnableAll_RollsBackOnFailure(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()

	rows := sqlmock.NewRows([]string{"schema", "table_name", "enabled"}).
		AddRow("public", "posts", false).
		AddRow("public", "comments", false)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)
	mock.ExpectBegin()
	mock.ExpectExec(testSetRealtime).WithArgs("public", "posts", true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(testSetRealtime).WithArgs("public", "comments", true).
		WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	svc := NewRealtimeService(db)
	if _, err := svc.EnableAll(context.Background()); err == nil {
		t.Fatal("EnableAll must report the failed table")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestRealtimeService_EnableAll_BeginError(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	rows := sqlmock.NewRows([]string{"schema", "table_name", "enabled"}).
		AddRow("public", "posts", false)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)
	mock.ExpectBegin().WillReturnError(errors.New("no tx"))

	svc := NewRealtimeService(db)
	if _, err := svc.EnableAll(context.Background()); err == nil {
		t.Fatal("EnableAll must report a failed BEGIN")
	}
}

func TestRealtimeService_EnableAll_NoOpWhenAllEnabled(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	rows := sqlmock.NewRows([]string{"schema", "table_name", "enabled"}).
		AddRow("public", "posts", true)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)

	svc := NewRealtimeService(db)
	n, err := svc.EnableAll(context.Background())
	if err != nil || n != 0 {
		t.Errorf("EnableAll: n=%d err=%v, want 0/nil", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestRealtimeService_DisableAll_DropsEnabledTables(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()

	rows := sqlmock.NewRows([]string{"schema", "table_name", "enabled"}).
		AddRow("public", "posts", true).
		AddRow("public", "comments", false)
	mock.ExpectQuery("SELECT").WillReturnRows(rows)
	mock.ExpectBegin()
	mock.ExpectExec(testSetRealtime).WithArgs("public", "posts", false).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	svc := NewRealtimeService(db)
	dropped, err := svc.DisableAll(context.Background())
	if err != nil {
		t.Fatalf("DisableAll: %v", err)
	}
	if dropped != 1 {
		t.Errorf("dropped: got %d, want 1", dropped)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
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

func TestOperatorSetup_SetupComplete(t *testing.T) {
	mock := k8s.NewMockClient()
	if !NewOperatorSetupService(mock).SetupComplete(context.Background()) {
		t.Error("the Postgres operator is installed under the mock, so setup is complete")
	}
}

// EXC-418: GET /api/setup/status is unauthenticated, so a loop on it used to
// be a loop on the Kubernetes apiserver. The answer is cached.
func TestOperatorSetup_SetupCompleteDoesNotAskTheClusterEveryTime(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	for i := 0; i < 20; i++ {
		svc.SetupComplete(context.Background())
	}
	if n := countMockCalls(mock, "GetDeployment"); n != 1 {
		t.Fatalf("asked the cluster %d times, want 1", n)
	}
}

// Installing an operator is exactly the event the cached answer must not
// outlive, so it drops the cache.
func TestOperatorSetup_InstallRefreshesTheCachedAnswer(t *testing.T) {
	mock := k8s.NewMockClient()
	svc := NewOperatorSetupService(mock)

	svc.SetupComplete(context.Background())
	before := countMockCalls(mock, "GetDeployment")
	if err := svc.InstallOperator(context.Background(), domain.PostgreSQL); err != nil {
		t.Fatalf("install: %v", err)
	}
	svc.SetupComplete(context.Background())
	if countMockCalls(mock, "GetDeployment") <= before {
		t.Fatal("the cached answer survived an install")
	}
}

func countMockCalls(mock *k8s.MockClient, prefix string) int {
	n := 0
	for _, call := range mock.Calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}
