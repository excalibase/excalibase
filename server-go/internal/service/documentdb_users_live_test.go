//go:build live

package service

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	corev1 "k8s.io/api/core/v1"
)

// EXC-427 against a real CNPG cluster, DocumentDB extension and gateway:
// a project's own Mongo users authenticate through the gateway with the roles
// they were given, cannot log in over SQL, and stop working when rotated,
// deleted, or carried into a newly registered (restored) cluster.
//
// Run with: go test ./internal/service/ -tags=live -run TestLiveMongoUsers -v -count=1 -timeout 60m
func TestLiveMongoUsersOnPostgres15(t *testing.T) { liveMongoUsers(t, "15") }

func TestLiveMongoUsersOnPostgres17(t *testing.T) { liveMongoUsers(t, "17") }

func liveMongoUsers(t *testing.T, major string) {
	lab := startDocumentDBLab(t)
	lab.installOperators(t)
	creds := lab.provisionMajor(t, major)
	lab.enableMajor(t, creds, major)
	lab.major = major
	lab.exposeGateway(t)
	lab.startMongoClient(t)

	t.Cleanup(func() {
		if t.Failed() {
			lab.dumpPodLogs(t)
		}
	})
	svc := lab.mongoUsersService(t, creds)
	ctx := lab.ctx
	started := lab.postmasterStart(t)
	writer, err := svc.CreateMongoUser(ctx, documentDBLiveName, "app_writer", MongoRoleReadWrite)
	if err != nil {
		t.Fatalf("create read-write user: %v", err)
	}
	reader, err := svc.CreateMongoUser(ctx, documentDBLiveName, "reporting", MongoRoleRead)
	if err != nil {
		t.Fatalf("create read-only user: %v", err)
	}

	// CNPG applies each user's peer line by reload: the server never restarted.
	if now := lab.postmasterStart(t); now != started {
		t.Fatalf("postgres restarted while users were mapped: %s -> %s", started, now)
	}
	t.Logf("postgres %s, no restart across two peer-map changes (started %s)", major, started)
	expectMongo(t, lab, writer.Username, writer.Password, mongoProbeScript, "inserted=true found=exc-454/1", "read-write insert+find")
	expectMongo(t, lab, reader.Username, reader.Password, mongoFindScript, "found=exc-454", "read-only find")
	refuseMongo(t, lab, reader.Username, reader.Password,
		`db.getSiblingDB("probe").items.insertOne({k: "reader"}); print("inserted-by-reader");`, "inserted-by-reader", "read-only insert")
	refuseMongo(t, lab, writer.Username, writer.Password,
		`db.getSiblingDB("admin").createUser({user: "sneaky", pwd: "sneaky-password-1", roles: [{role: "readWriteAnyDatabase", db: "admin"}, {role: "clusterAdmin", db: "admin"}]}); print("created-sneaky");`,
		"created-sneaky", "a Mongo user creating users")

	lab.expectSQLRefused(t, writer.Username, writer.Password)
	lab.expectSQLRefused(t, reader.Username, reader.Password)
	lab.expectSQLAccepted(t, creds.Username, creds.Password)

	rotated, err := svc.RotateMongoUser(ctx, documentDBLiveName, reader.Username)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	refuseMongo(t, lab, reader.Username, reader.Password, mongoFindScript, "found=", "the pre-rotation password")
	expectMongo(t, lab, reader.Username, rotated.Password, mongoFindScript, "found=exc-454", "the rotated password")

	if err := svc.DeleteMongoUser(ctx, documentDBLiveName, writer.Username); err != nil {
		t.Fatalf("delete: %v", err)
	}
	refuseMongo(t, lab, writer.Username, writer.Password, mongoFindScript, "found=", "a deleted user")
	if lines := lab.peerLines(t); strings.Contains(lines, "app_writer") || !strings.Contains(lines, "reporting") {
		t.Fatalf("peer lines after delete: %s", lines)
	}
	expectMongo(t, lab, creds.Username, creds.Password, mongoFindScript, "found=exc-454", "the owner after a user was deleted (data kept)")

	// A restored cluster arrives with the source's users; registering it drops them.
	if err := svc.enableDocumentDB(ctx, lab.instance(creds), idleContext()); err != nil {
		t.Fatalf("registration step: %v", err)
	}
	refuseMongo(t, lab, reader.Username, rotated.Password, mongoFindScript, "found=", "a user carried into a newly registered cluster")
	expectMongo(t, lab, creds.Username, creds.Password, mongoFindScript, "found=exc-454", "the owner after registration")

	lab.expectDeletionWithinWindow(t)
}

func (lab *documentDBLab) instance(creds *provisioner.ProvisioningResult) *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID: documentDBLiveName, OrgID: documentDBLiveOrg, Namespace: documentDBLiveNS,
		DatabaseName: creds.DatabaseName, Username: creds.Username, PostgresVersion: lab.major,
		DocumentDB: true, DeploymentMode: domain.ModeK8s, DBType: domain.PostgreSQL, Status: "ACTIVE",
	}
}

func (lab *documentDBLab) mongoUsersService(t *testing.T, creds *provisioner.ProvisioningResult) *ProvisioningService {
	t.Helper()
	store := documentDBStore(t)
	if err := store.Create(lab.instance(creds)); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	svc := NewProvisioningService(store, provisioner.NewFactory(), lab.client)
	svc.SetVault(newFakeVault())
	return svc
}

func expectMongo(t *testing.T, lab *documentDBLab, user, password, script, want, what string) {
	t.Helper()
	out := lab.mongo(t, user, password, script)
	if !strings.Contains(out, want) {
		t.Fatalf("%s: want %q, got %s", what, want, out)
	}
	t.Logf("%s: %s", what, out)
}

func refuseMongo(t *testing.T, lab *documentDBLab, user, password, script, success, what string) {
	t.Helper()
	out := lab.mongo(t, user, password, script)
	if strings.Contains(out, success) {
		t.Fatalf("%s was allowed: %s", what, out)
	}
	t.Logf("%s refused: %s", what, out)
}

// sqlLogin dials the read-write Service over the pod network, the path the
// public port also arrives by, so pg_hba sees a network login.
func (lab *documentDBLab) sqlLogin(user, password string) (string, error) {
	conn := "host=" + documentDBLiveName + "-postgres-rw port=5432 dbname=postgres sslmode=require user=" + user
	return lab.client.ExecInPodStdin(lab.ctx, documentDBLiveNS, documentDBLiveName+"-postgres-1", "postgres",
		[]string{"sh", "-c", `PGPASSWORD="$(cat)" psql "` + conn + `" -tAc "SELECT 'sql-login-ok'"`}, password)
}

func (lab *documentDBLab) expectSQLRefused(t *testing.T, user, password string) {
	t.Helper()
	out, err := lab.sqlLogin(user, password)
	if err == nil || strings.Contains(out, "sql-login-ok") {
		t.Fatalf("%s logged in over SQL: %s", user, out)
	}
	if !strings.Contains(err.Error(), "pg_hba.conf rejects connection") {
		t.Fatalf("%s refused for another reason: %v", user, err)
	}
	t.Logf("%s over SQL refused: %v", user, err)
}

func (lab *documentDBLab) expectSQLAccepted(t *testing.T, user, password string) {
	t.Helper()
	out, err := lab.sqlLogin(user, password)
	if err != nil || !strings.Contains(out, "sql-login-ok") {
		t.Fatalf("the owner over SQL: %s %v", out, err)
	}
	t.Logf("%s over SQL: accepted (control)", user)
}

func (lab *documentDBLab) dumpPodLogs(t *testing.T) {
	for _, container := range []string{k8s.DocumentDBGatewayContainer, "postgres"} {
		tail := int64(80)
		raw, err := lab.cs.CoreV1().Pods(documentDBLiveNS).GetLogs(documentDBLiveName+"-postgres-1",
			&corev1.PodLogOptions{Container: container, TailLines: &tail}).DoRaw(lab.ctx)
		t.Logf("---- %s logs (err=%v) ----\n%s", container, err, raw)
	}
}

func (lab *documentDBLab) postmasterStart(t *testing.T) string {
	t.Helper()
	out, err := lab.client.ExecInPod(lab.ctx, documentDBLiveNS, documentDBLiveName+"-postgres-1", "postgres",
		[]string{"psql", "-U", "postgres", "-tAc", "SELECT pg_postmaster_start_time()"})
	if err != nil {
		t.Fatalf("postmaster start: %v", err)
	}
	return strings.TrimSpace(out)
}

func (lab *documentDBLab) peerLines(t *testing.T) string {
	t.Helper()
	out, err := lab.client.ExecInPod(lab.ctx, documentDBLiveNS, documentDBLiveName+"-postgres-1", "postgres",
		[]string{"psql", "-U", "postgres", "-tAc", "SELECT string_agg(pg_username, ',') FROM pg_ident_file_mappings"})
	if err != nil {
		t.Fatalf("peer lines: %v", err)
	}
	return strings.TrimSpace(out)
}
