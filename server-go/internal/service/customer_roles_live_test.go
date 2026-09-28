//go:build live

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/pgscram"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

// Against a real CloudNativePG DocumentDB cluster with backups: the
// owner creates a customer role (plain SQL on 17, excalibase.create_role on
// 15), the role logs in by password over TLS only and reads what the owner
// granted, Studio's statement works over the platform's certificate login,
// every escalation is refused, the platform's roles and Mongo users still
// work, and a restore brings the customer roles back while the platform's
// restore logic drops only the Mongo users.
//
// Run with: go test ./internal/service/ -tags=live -run TestLiveCustomerRoles -v -count=1 -timeout 90m
func TestLiveCustomerRolesOnPostgres15(t *testing.T) { liveCustomerRoles(t, "15") }

func TestLiveCustomerRolesOnPostgres17(t *testing.T) { liveCustomerRoles(t, "17") }

const (
	custReader         = "cust_reader"
	custReaderPassword = "Cust-Reader-Pass-1"
	studioRole         = "studio_role"
	studioRolePassword = "Studio-Role-Pass-1"
	liveMongoReader    = "reporting"
	liveCertDir        = "/controller/run/exc527"
)

// customerRolesLab is one project's cluster plus the service that registered it.
type customerRolesLab struct {
	*backupLab
	svc       *ProvisioningService
	vault     *fakeVault
	registrar *liveRegistrar
	major     string
	sqlRoles  bool
}

func liveCustomerRoles(t *testing.T, major string) {
	lab := &customerRolesLab{backupLab: &backupLab{documentDBLab: startDocumentDBLab(t), suffix: randomHex(t, 3)},
		major: major, sqlRoles: major >= "16"}
	lab.documentDBLab.major = major
	lab.sourceID = documentDBLiveName
	lab.installBackupStack(t)
	lab.installOperators(t)
	t.Cleanup(func() {
		if t.Failed() {
			lab.dumpPodLogs(t)
		}
		lab.purgePrefixes(t)
	})
	ownerPassword := lab.provisionAndRegister(t)
	lab.exposeGateway(t)
	lab.startMongoClient(t)
	if _, err := lab.svc.CreateMongoUser(lab.ctx, documentDBLiveName, liveMongoReader, MongoRoleRead); err != nil {
		t.Fatalf("create Mongo user: %v", err)
	}

	lab.ownerCreatesCustomerRole(t, ownerPassword)
	lab.customerRoleLogsInOnlyOverTLS(t, documentDBLiveName, custReaderPassword)
	lab.studioCreatesARole(t)
	lab.escalationsAreRefused(t, ownerPassword)
	lab.platformStillWorks(t, ownerPassword)

	lab.write(t, documentDBLiveName, 1, 10)
	backupID := lab.takeBackup(t)
	restored := "crrestore" + lab.suffix
	restoredOwnerPassword := lab.restoreThroughRegistration(t, restored, backupID)
	lab.restoreKeptCustomerRoles(t, restored, restoredOwnerPassword)
}

// liveRegistrar runs the platform's own role logic, the part of registration
// that touches a restored cluster's roles, then records the row.
type liveRegistrar struct {
	svc  *ProvisioningService
	rows *fakeRegistrar
	last *domain.DatabaseInstance
}

func (r *liveRegistrar) RegisterProject(ctx context.Context, inst *domain.DatabaseInstance, opts RegistrationOptions) error {
	pc := idleContext()
	if err := r.svc.setupProjectCredentials(ctx, inst, opts, pc); err != nil {
		return err
	}
	if err := r.svc.enableDocumentDB(ctx, inst, pc); err != nil {
		return err
	}
	r.last = inst
	return r.rows.RegisterProject(ctx, inst, opts)
}

func customerRolesTier() config.TierConfig {
	return config.TierConfig{Instances: 1, StorageSize: "1Gi", Memory: "1Gi", CPU: "0.5", StatementTimeout: "30s", BackupEnabled: true}
}

// provisionAndRegister builds a DocumentDB project with backups on, then
// registers it as provisioning does. It returns the owner's password.
func (lab *customerRolesLab) provisionAndRegister(t *testing.T) string {
	t.Helper()
	minted, err := lab.issuer.ForNewProject(lab.ctx, lab.store, documentDBLiveName)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	req := domain.ProvisioningRequest{
		ProjectName: documentDBLiveName, OrgID: backupLiveOrg, DBType: domain.PostgreSQL, PostgresVersion: lab.major,
		DatabaseName: "app", MasterUsername: documentDBLiveOwner, DocumentDB: true,
		Backup: &domain.BackupSettings{Enabled: true, Schedule: backupLiveSchedule, Retention: 7, S3: minted},
	}
	creds, err := provisioner.NewPostgreSQLProvisioner(lab.client, "").
		ProvisionWithRollback(lab.ctx, req, customerRolesTier(), provisioner.NewProvisionContext(nil, nil))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	enabled := true
	lab.source = &domain.DatabaseInstance{
		ProjectID: documentDBLiveName, OrgID: backupLiveOrg, Namespace: documentDBLiveNS,
		DBType: domain.PostgreSQL, PostgresVersion: lab.major, DeploymentMode: domain.ModeK8s, DocumentDB: true,
		DatabaseName: creds.DatabaseName, Username: creds.Username, Password: creds.Password, BackupEnabled: &enabled,
	}
	lab.vault = newFakeVault()
	lab.svc = NewProvisioningService(lab.instances, provisioner.NewFactory(), lab.client)
	lab.svc.SetVault(lab.vault)
	lab.registrar = &liveRegistrar{svc: lab.svc, rows: &fakeRegistrar{store: lab.instances}}
	if err := lab.registrar.RegisterProject(lab.ctx, lab.source, RegistrationOptions{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	eventuallyLive(t, "gateway accepting connections", 8*time.Minute, func() bool {
		ready, err := lab.client.DocumentDBGatewayReady(lab.ctx, documentDBLiveNS, documentDBLiveName+"-postgres-1")
		return err == nil && ready && lab.gatewayLogged(t, "Gateway ready to accept connections")
	})
	lab.adapter = NewK8sBackupAdapter(lab.client, StaticBackupStorage(lab.store))
	lab.adapter.SetBackupCredentials(lab.issuer)
	lab.psql(t, documentDBLiveName, "CREATE TABLE customers (id int PRIMARY KEY, name text NOT NULL);"+
		"CREATE TABLE orders (id int PRIMARY KEY, customer_id int REFERENCES customers(id), amount numeric(10,2));")
	return creds.Password
}

// sqlOver logs in over the pod network through the read-write Service, the
// way the public port arrives too. Password and statements go on stdin.
func (lab *customerRolesLab) sqlOver(project, user, password, sslmode, statements string) (string, error) {
	conn := fmt.Sprintf("host=%s-postgres-rw port=5432 dbname=app sslmode=%s user=%s", project, sslmode, user)
	return lab.client.ExecInPodStdin(lab.ctx, backupLiveOrg+"-"+project, project+"-postgres-1", "postgres",
		[]string{"sh", "-c", `IFS= read -r PGPASSWORD; export PGPASSWORD; psql "$0" -v ON_ERROR_STOP=1 -tA -f -`, conn},
		password+"\n"+statements+"\n")
}

func (lab *customerRolesLab) mustSQL(t *testing.T, project, user, password, statements string) string {
	t.Helper()
	out, err := lab.sqlOver(project, user, password, "require", statements)
	if err != nil {
		t.Fatalf("%s: %s: %v\n%s", user, statements, err, out)
	}
	return strings.TrimSpace(out)
}

func (lab *customerRolesLab) refusedSQL(t *testing.T, project, user, password, statement string) {
	t.Helper()
	out, err := lab.sqlOver(project, user, password, "require", statement)
	if err == nil {
		t.Fatalf("%s was allowed: %s\n%s", user, statement, out)
	}
	t.Logf("refused %s: %s -> %s", user, statement, lastLine(out))
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return lines[len(lines)-1]
}

func (lab *customerRolesLab) ownerCreatesCustomerRole(t *testing.T, ownerPassword string) {
	t.Helper()
	create := "SELECT excalibase.create_role('" + custReader + "', '" + custReaderPassword + "')"
	if lab.sqlRoles {
		create = "CREATE ROLE " + custReader + " LOGIN PASSWORD '" + custReaderPassword + "'"
	} else {
		lab.refusedSQL(t, documentDBLiveName, documentDBLiveOwner, ownerPassword, "CREATE ROLE direct_role LOGIN")
	}
	lab.mustSQL(t, documentDBLiveName, documentDBLiveOwner, ownerPassword, create+";\n"+
		"CREATE TABLE ledger (id int PRIMARY KEY, note text);\n"+
		"INSERT INTO ledger VALUES (1, 'visible-to-customer-role');\n"+
		"GRANT SELECT ON ledger TO "+custReader+";")
	t.Logf("postgres %s: owner created %s (%s)", lab.major, custReader, map[bool]string{true: "SQL", false: "function"}[lab.sqlRoles])
}

// customerRoleLogsInOnlyOverTLS: TLS and the right password, nothing else.
func (lab *customerRolesLab) customerRoleLogsInOnlyOverTLS(t *testing.T, project, password string) {
	t.Helper()
	got := lab.mustSQL(t, project, custReader, password, "SELECT note FROM ledger WHERE id = 1; SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid();")
	if got != "visible-to-customer-role\nt" {
		t.Fatalf("customer role over TLS read %q", got)
	}
	lab.loginRefused(t, project, password, "disable", "pg_hba.conf rejects connection")
	lab.loginRefused(t, project, "wrong-password", "require", "password authentication failed")
	lab.refusedSQL(t, project, custReader, password, "UPDATE ledger SET note = 'x'")
	t.Logf("%s: TLS login ok, plaintext refused by pg_hba, wrong password refused, read-only as granted", custReader)
}

func (lab *customerRolesLab) loginRefused(t *testing.T, project, password, sslmode, reason string) {
	t.Helper()
	out, err := lab.sqlOver(project, custReader, password, sslmode, "SELECT 'login-ok'")
	if err == nil || !strings.Contains(out+err.Error(), reason) {
		t.Fatalf("%s login (sslmode=%s) not refused with %q: %v\n%s", custReader, sslmode, reason, err, out)
	}
}

// certLogin logs in as a platform role with the certificate its vault record holds.
func (lab *customerRolesLab) certLogin(t *testing.T, project, role, statements string) (string, error) {
	t.Helper()
	record, err := lab.vault.Get(vaultCredentialPath(project, role))
	if err != nil {
		t.Fatalf("vault %s: %v", role, err)
	}
	namespace, pod := backupLiveOrg+"-"+project, project+"-postgres-1"
	for name, field := range map[string]string{"crt": tenantcert.FieldCert, "key": tenantcert.FieldKey, "ca": tenantcert.FieldRootCert} {
		if _, err := lab.client.ExecInPodStdin(lab.ctx, namespace, pod, "postgres",
			[]string{"sh", "-c", "umask 077; mkdir -p " + liveCertDir + " && cat > " + liveCertDir + "/" + role + "." + name}, record[field]); err != nil {
			t.Fatalf("write %s %s: %v", role, name, err)
		}
	}
	conn := fmt.Sprintf("host=%s-postgres-rw port=5432 dbname=app sslmode=verify-full user=%s sslcert=%s/%s.crt sslkey=%s/%s.key sslrootcert=%s/%s.ca",
		project, role, liveCertDir, role, liveCertDir, role, liveCertDir, role)
	return lab.client.ExecInPodStdin(lab.ctx, namespace, pod, "postgres",
		[]string{"sh", "-c", `psql "$0" -v ON_ERROR_STOP=1 -tA -f -`, conn}, statements+"\n")
}

// studioCreatesARole sends Studio's statement as excalibase_app, logged in
// by its certificate, with the password as its SCRAM verifier.
func (lab *customerRolesLab) studioCreatesARole(t *testing.T) {
	t.Helper()
	verifier, err := pgscram.Verifier(studioRolePassword)
	if err != nil {
		t.Fatal(err)
	}
	out, err := lab.certLogin(t, documentDBLiveName, roleApp,
		"SELECT excalibase.create_role('"+studioRole+"', '"+verifier+"', true)")
	if err != nil {
		t.Fatalf("Studio create_role as excalibase_app: %v\n%s", err, out)
	}
	if got := lab.mustSQL(t, documentDBLiveName, studioRole, studioRolePassword, "SELECT current_user"); got != studioRole {
		t.Fatalf("Studio's role logged in as %q", got)
	}
	out, err = lab.certLogin(t, documentDBLiveName, roleApp, "SELECT excalibase.drop_role('"+liveMongoReader+"')")
	if err == nil {
		t.Fatalf("Studio dropped a Mongo user: %s", out)
	}
	t.Logf("Studio (excalibase_app by certificate) created %s; dropping Mongo user refused: %s", studioRole, lastLine(out))
}

func (lab *customerRolesLab) escalationsAreRefused(t *testing.T, ownerPassword string) {
	t.Helper()
	owner := func(statement string) {
		lab.refusedSQL(t, documentDBLiveName, documentDBLiveOwner, ownerPassword, statement)
	}
	for _, predefined := range []string{"pg_execute_server_program", "pg_read_server_files", "pg_write_server_files", "pg_read_all_data", "pg_signal_backend"} {
		owner("GRANT " + predefined + " TO " + documentDBLiveOwner)
		owner("GRANT " + predefined + " TO " + custReader)
	}
	platform := []string{roleApp, roleAuthAdmin, roleWatcher, config.DocumentDBGatewayRole, config.DocumentDBMongoUsersGroup,
		DocBrowserLogin, liveMongoReader, "streaming_replica", "postgres"}
	if lab.sqlRoles {
		for _, role := range platform {
			owner("ALTER ROLE " + role + " PASSWORD 'Taken-Pass-1'")
			owner("ALTER ROLE " + role + " NOLOGIN")
			owner("DROP ROLE " + role)
			owner("GRANT " + role + " TO " + custReader)
		}
		for _, attribute := range []string{"SUPERUSER", "REPLICATION", "BYPASSRLS", "CREATEDB"} {
			owner("CREATE ROLE privileged_role " + attribute)
		}
		owner("CREATE ROLE sneaky_mongo LOGIN IN ROLE " + config.DocumentDBMongoUsersGroup)
	}
	for _, role := range platform {
		owner("SELECT excalibase.alter_role_password('" + role + "', 'Taken-Pass-1')")
		owner("SELECT excalibase.drop_role('" + role + "')")
		owner("SELECT excalibase.create_role('escalate', 'Escalate-Pass-1', true, ARRAY['" + role + "'])")
	}
	owner("SELECT excalibase.create_role('excalibase_squat', 'Squat-Pass-1')")
	// A Mongo createUser has to grant a DocumentDB role; the owner holds no ADMIN OPTION on one.
	refuseMongo(t, lab.documentDBLab, documentDBLiveOwner, ownerPassword,
		`db.getSiblingDB("admin").createUser({user: "sneaky", pwd: "sneaky-password-1", roles: [{role: "readAnyDatabase", db: "admin"}]}); print("created-sneaky");`,
		"created-sneaky", "the owner creating a Mongo user")
	if got := strings.TrimSpace(lab.psql(t, documentDBLiveName,
		"SELECT count(*) FROM pg_roles WHERE rolname IN ('sneaky', 'escalate', 'excalibase_squat', 'privileged_role', 'sneaky_mongo', 'direct_role') OR rolsuper AND rolname <> 'postgres'")); got != "0" {
		t.Fatalf("an escalation left %s role(s) behind", got)
	}
	if got := strings.TrimSpace(lab.psql(t, documentDBLiveName,
		"SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.roleid JOIN pg_roles u ON u.oid = m.member "+
			"WHERE u.rolname IN ('"+documentDBLiveOwner+"', '"+custReader+"') AND (r.rolname LIKE 'pg\\_%' OR r.rolname IN ('excalibase_mongo_users', 'excalibase_app', 'auth_admin', 'cdc_watcher'))")); got != "0" {
		t.Fatalf("the owner or its role holds %s forbidden membership(s)", got)
	}
	t.Logf("postgres %s: every escalation refused", lab.major)
}

// platformStillWorks: the platform's roles log in by certificate, the Mongo
// user and the owner still work through the gateway.
func (lab *customerRolesLab) platformStillWorks(t *testing.T, ownerPassword string) {
	t.Helper()
	for _, role := range tenantcert.PlatformRoles {
		out, err := lab.certLogin(t, documentDBLiveName, role, "SELECT current_user")
		if err != nil || strings.TrimSpace(out) != role {
			t.Fatalf("%s certificate login: %v\n%s", role, err, out)
		}
	}
	expectMongo(t, lab.documentDBLab, documentDBLiveOwner, ownerPassword, mongoProbeScript, "inserted=true", "the owner through the gateway")
	reader, err := lab.vault.Get(mongoUserVaultPath(documentDBLiveName, liveMongoReader))
	if err != nil {
		t.Fatal(err)
	}
	expectMongo(t, lab.documentDBLab, liveMongoReader, reader["password"], mongoFindScript, "found=exc-454", "the Mongo user")
	lab.expectSQLRefused(t, liveMongoReader, reader["password"])
}

// restoreThroughRegistration restores the backup into a new project and
// registers it with the platform's role logic. It returns the new owner password.
func (lab *customerRolesLab) restoreThroughRegistration(t *testing.T, project, backupID string) string {
	t.Helper()
	lab.adapter.SetInstanceStore(lab.instances)
	lab.adapter.SetProjectRegistrar(lab.registrar)
	lab.adapter.SetDatabaseProbe(psqlProbe{lab: lab.backupLab})
	lab.adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{
		Tier: domain.Free, Config: customerRolesTier(),
		Backup: &domain.BackupSettings{Enabled: true, Schedule: backupLiveSchedule, Retention: 7},
	}})
	started := time.Now()
	if _, err := lab.adapter.Restore(lab.ctx, lab.source, domain.RestoreRequest{BackupID: backupID, NewProjectName: project, TargetProjectID: project}); err != nil {
		t.Fatalf("restore %s: %v", project, err)
	}
	if lab.registrar.last == nil || lab.registrar.last.ProjectID != project {
		t.Fatal("the restore did not go through registration")
	}
	t.Logf("restore %s registered in %s", project, time.Since(started).Round(time.Second))
	return lab.registrar.last.Password
}

// restoreKeptCustomerRoles: the customer's roles came back with their
// passwords and grants, the Mongo user did not, and the new owner still
// manages its roles.
func (lab *customerRolesLab) restoreKeptCustomerRoles(t *testing.T, project, ownerPassword string) {
	t.Helper()
	lab.customerRoleLogsInOnlyOverTLS(t, project, custReaderPassword)
	if got := lab.mustSQL(t, project, studioRole, studioRolePassword, "SELECT current_user"); got != studioRole {
		t.Fatalf("Studio's role after restore: %q", got)
	}
	if got := strings.TrimSpace(lab.psql(t, project, "SELECT count(*) FROM pg_roles WHERE rolname = '"+liveMongoReader+"'")); got != "0" {
		t.Fatalf("the source's Mongo user survived the restore (%s)", got)
	}
	change := "SELECT excalibase.alter_role_password('" + custReader + "', 'Cust-Reader-Pass-2')"
	if lab.sqlRoles {
		change = "ALTER ROLE " + custReader + " PASSWORD 'Cust-Reader-Pass-2'"
	}
	lab.mustSQL(t, project, documentDBLiveOwner, ownerPassword, change)
	if got := lab.mustSQL(t, project, custReader, "Cust-Reader-Pass-2", "SELECT current_user"); got != custReader {
		t.Fatalf("re-passworded customer role: %q", got)
	}
	for _, role := range tenantcert.PlatformRoles {
		out, err := lab.certLogin(t, project, role, "SELECT current_user")
		if err != nil || strings.TrimSpace(out) != role {
			t.Fatalf("restored %s certificate login: %v\n%s", role, err, out)
		}
	}
	t.Logf("postgres %s restore: customer roles back with their passwords and grants, Mongo user dropped, owner manages its roles", lab.major)
}
