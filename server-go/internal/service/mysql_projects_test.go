package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// ADR 0041, EXC-583: MySQL projects on MOCO.

func setupMySQLTest(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	svc, store, mock := setupProvisioningTest(t)
	mock.AutoReconcileClusters = true
	mock.WildcardExecOutput = "1\n"
	mysql := provisioner.NewMySQLProvisioner(mock)
	poll := provisioner.NewPoller(time.Millisecond, 100*time.Millisecond)
	mysql.SetPollers(poll, poll)
	svc.factory = provisioner.NewFactory(provisioner.NewPostgreSQLProvisioner(mock, ""), mysql)
	svc.AllowMySQLCreation(true)
	svc.SetBackgroundRunner(func(run func()) { run() })
	return svc, store, mock
}

func mysqlProvisionRequest(version string) domain.ProvisioningRequest {
	return domain.ProvisioningRequest{ProjectName: "shop", OrgID: "org1", DBType: domain.MySQL, MySQLVersion: version}
}

func TestValidateMySQLRequest(t *testing.T) {
	if err := validateProvisioningRequest(mysqlProvisionRequest("")); err != nil {
		t.Errorf("a MySQL request naming no version takes the newest: %v", err)
	}
	if err := validateProvisioningRequest(mysqlProvisionRequest("8.4.9")); err != nil {
		t.Errorf("a catalogued version: %v", err)
	}
	refused := map[string]func(*domain.ProvisioningRequest){
		"unknown version":  func(r *domain.ProvisioningRequest) { r.MySQLVersion = "8.0.46" },
		"postgres version": func(r *domain.ProvisioningRequest) { r.PostgresVersion = "17" },
		"documentdb":       func(r *domain.ProvisioningRequest) { r.DocumentDB = true },
		"postgres settings": func(r *domain.ProvisioningRequest) {
			r.Parameters = map[string]string{"work_mem": "4MB"}
		},
		"backups": func(r *domain.ProvisioningRequest) { r.Backup = &domain.BackupSettings{Enabled: true} },
	}
	for name, change := range refused {
		req := mysqlProvisionRequest("")
		change(&req)
		if err := validateProvisioningRequest(req); err == nil {
			t.Errorf("%s was accepted on a MySQL project", name)
		}
	}
	pg := provisionRequest("17")
	pg.MySQLVersion = "8.4.10"
	if err := validateProvisioningRequest(pg); err == nil {
		t.Error("a Postgres request naming a MySQL version was accepted")
	}
}

// With the feature off a MySQL request is refused before any row or
// credential exists.
func TestProvisionMySQLRefusedWhenNotRegistered(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	for _, creation := range []bool{false, true} {
		svc.AllowMySQLCreation(creation)
		if _, err := svc.Provision(context.Background(), mysqlProvisionRequest("")); err == nil {
			t.Fatalf("creation %v: a MySQL project was created with no MySQL provisioner", creation)
		}
	}
	svc, store, _ = setupMySQLTest(t)
	svc.AllowMySQLCreation(false)
	if _, err := svc.Provision(context.Background(), mysqlProvisionRequest("")); err == nil ||
		!strings.Contains(err.Error(), "unsupported database type") {
		t.Fatalf("err = %v", err)
	}
	if all, _ := store.FindAll(); len(all) != 0 {
		t.Errorf("a refused request left %d rows", len(all))
	}
}

func provisionMySQL(t *testing.T, svc *ProvisioningService, store *storage.FileSystemStore, version string) *domain.DatabaseInstance {
	t.Helper()
	resp, err := svc.Provision(context.Background(), mysqlProvisionRequest(version))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if resp.Status != "ACTIVE" {
		t.Fatalf("status %s: %s", resp.Status, resp.FailureReason)
	}
	inst, err := store.FindByProjectID(resp.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func TestProvisionMySQLProject(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	if inst.DBType != domain.MySQL || inst.MySQLVersion != config.DefaultMySQLVersion() || inst.PostgresVersion != "" {
		t.Errorf("recorded %s mysql=%q postgres=%q", inst.DBType, inst.MySQLVersion, inst.PostgresVersion)
	}
	if inst.Host != k8s.MySQLPrimaryHost(inst.ProjectID, inst.Namespace) || inst.Port == nil || *inst.Port != 3306 {
		t.Errorf("endpoint %s:%v", inst.Host, inst.Port)
	}
	if inst.BackupEnabled != nil && *inst.BackupEnabled {
		t.Error("a MySQL project recorded backups it does not have yet")
	}
	for _, cmd := range mock.ExecCommands {
		if strings.Contains(cmd, "psql") {
			t.Errorf("Postgres SQL ran against a MySQL project: %s", cmd)
		}
	}
	if len(mock.HelmReleases) != 0 {
		t.Errorf("a watcher was deployed for a MySQL project: %v", mock.HelmReleases)
	}
	if _, ok := mock.CRDs[inst.Namespace+"/"+inst.ProjectID+"-mysql"]; !ok {
		t.Error("no MySQLCluster")
	}
}

func TestUpgradeMySQLMovesToTheNewestPatch(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "8.4.9")
	newest := config.DefaultMySQLVersion()
	if err := svc.UpgradeVersion(context.Background(), inst.ProjectID, newest); err != nil {
		t.Fatalf("UpgradeVersion: %v", err)
	}
	image, _ := config.MySQLImage(newest)
	if got := k8s.MySQLClusterImage(mock.CRDs[inst.Namespace+"/"+inst.ProjectID+"-mysql"]); got != image {
		t.Errorf("image %s", got)
	}
	again, _ := store.FindByProjectID(inst.ProjectID)
	if again.MySQLVersion != newest {
		t.Errorf("recorded %s", again.MySQLVersion)
	}
	if err := svc.UpgradeVersion(context.Background(), inst.ProjectID, newest); err != nil {
		t.Errorf("repeating the upgrade it already has: %v", err)
	}
	if err := svc.UpgradeVersion(context.Background(), inst.ProjectID, "8.4.9"); err == nil {
		t.Error("a downgrade was accepted")
	}
}

func TestApplyOrgTierScalesAMySQLProjectUp(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	setOrgTier(svc, "org1", domain.Standard)
	if err := svc.ApplyOrgTier(context.Background(), inst.ProjectID, domain.Standard); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
	cluster := mock.CRDs[inst.Namespace+"/"+inst.ProjectID+"-mysql"]
	if replicas, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas"); replicas != 3 {
		t.Errorf("replicas %d", replicas)
	}
	again, _ := store.FindByProjectID(inst.ProjectID)
	if again.Tier != domain.Standard {
		t.Errorf("tier %s", again.Tier)
	}
	setOrgTier(svc, "org1", domain.Free)
	if err := svc.ApplyOrgTier(context.Background(), inst.ProjectID, domain.Free); !errors.Is(err, k8s.ErrMySQLScaleDown) {
		t.Errorf("Standard → Free: %v", err)
	}
}

// Changes only a Postgres cluster takes are refused, not attempted; the
// plan a MySQL cluster runs reads like any other.
func TestPostgresOnlyClusterChangesRefuseMySQL(t *testing.T) {
	svc, store, _ := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	if err := svc.TuneParameters(context.Background(), inst.ProjectID, map[string]string{"work_mem": "8MB"}); !errors.Is(err, ErrNotForMySQL) {
		t.Errorf("TuneParameters: %v", err)
	}
	if err := svc.ResizeStorage(context.Background(), inst.ProjectID, "10Gi"); !errors.Is(err, ErrNotForMySQL) {
		t.Errorf("ResizeStorage: %v", err)
	}
	settings, err := svc.ClusterSettings(context.Background(), inst.ProjectID)
	if err != nil || settings.Instances != 1 || settings.Memory != "1Gi" || len(settings.TunableParameters) != 0 {
		t.Errorf("ClusterSettings: %+v %v", settings, err)
	}
}

func TestDeprovisionMySQLLeavesNothing(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	if err := svc.SetDeletionProtection(inst.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deprovision(context.Background(), inst.ProjectID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if mock.Namespaces[inst.Namespace] {
		t.Error("namespace left")
	}
	if slices.ContainsFunc(mock.Calls, func(c string) bool { return strings.Contains(c, "-postgres") }) {
		t.Errorf("a Postgres resource was touched: %v", mock.Calls)
	}
	if got, _ := store.FindByProjectID(inst.ProjectID); got != nil {
		t.Error("row left")
	}
}

// A resumed MySQL project has no CDC watcher to put back (realtime on MySQL
// is a later phase); the Postgres watcher must never be installed for it.
func TestRestartReplicationSkipsMySQL(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	inst := provisionMySQL(t, svc, store, "")
	if err := svc.RestartReplication(context.Background(), inst); err != nil {
		t.Fatalf("RestartReplication: %v", err)
	}
	if len(mock.HelmReleases) != 0 {
		t.Errorf("a watcher was installed: %v", mock.HelmReleases)
	}
}

// A project created without a database can take a MySQL one later.
func TestAddingAMySQLDatabaseToAProjectWithout(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	created := createWithoutDatabase(t, svc)
	if _, err := svc.AddDatabase(context.Background(), created.ProjectID, domain.ProvisioningRequest{DBType: domain.MySQL}); err != nil {
		t.Fatalf("AddDatabase: %v", err)
	}
	inst, _ := store.FindByProjectID(created.ProjectID)
	if inst.NoDatabase || inst.DBType != domain.MySQL || inst.MySQLVersion != config.DefaultMySQLVersion() || inst.Status != "ACTIVE" {
		t.Errorf("recorded noDatabase=%v %s %q %s", inst.NoDatabase, inst.DBType, inst.MySQLVersion, inst.Status)
	}
	if _, ok := mock.CRDs[inst.Namespace+"/"+inst.ProjectID+"-mysql"]; !ok {
		t.Error("no MySQLCluster")
	}
}

// A MySQL Free project is admitted on what it really asks for, MySQL's 1Gi
// floor, not the 512Mi a Postgres Free project fits in.
func TestMySQLIsAdmittedOnItsMemoryFloor(t *testing.T) {
	svc, _, mock := setupMySQLTest(t)
	svc.capacityHeadroomPercent = 0
	free, _ := config.GetTierConfig(domain.Free)
	mysqlFree, _ := config.MySQLPlan(free)
	_, postgresMem, _ := TierResourceFootprint(free)
	_, mysqlMem, _ := TierResourceFootprint(mysqlFree)
	room := postgresMem + (mysqlMem-postgresMem)/2
	mock.Capacity = k8s.ClusterCapacity{AllocatableCPUMilli: 32000, AllocatableMemBytes: room,
		Nodes: []k8s.NodeCapacity{{Name: "node-0", AllocatableCPUMilli: 32000, AllocatableMemBytes: room}}}

	if _, err := svc.Provision(context.Background(), mysqlProvisionRequest("")); err == nil {
		t.Fatal("a MySQL project was admitted without room for its 1Gi")
	}
	if _, err := svc.Provision(context.Background(), provisionRequest("17")); err != nil {
		t.Errorf("a Postgres Free project fits the same room: %v", err)
	}
}

// A MySQL add interrupted by a restart is cleaned up by the MySQL
// provisioner; the Postgres one would leave the MOCO cluster behind.
func TestRecoveringAnInterruptedMySQLAddRemovesTheMOCOCluster(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	created := createWithoutDatabase(t, svc)
	inst, _ := store.FindByProjectID(created.ProjectID)
	image, _ := config.MySQLImage(config.DefaultMySQLVersion())
	cluster, _ := k8s.BuildMySQLCluster(k8s.MySQLClusterOpts{ProjectID: inst.ProjectID, Namespace: inst.Namespace, Image: image,
		Tier: config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "1Gi", CPU: "0.5"}})
	if err := mock.ApplyCRD(context.Background(), k8s.MOCOClusterGVR, inst.Namespace, cluster); err != nil {
		t.Fatal(err)
	}
	inst.DBType, inst.MySQLVersion, inst.Status = domain.MySQL, config.DefaultMySQLVersion(), string(domain.StatusProvisioning)
	if err := store.UpdateIfStatus(inst, string(domain.StatusActive)); err != nil {
		t.Fatal(err)
	}
	if recovered := svc.RecoverInterruptedDatabaseAdds(context.Background()); len(recovered) != 1 {
		t.Fatalf("recovered %v", recovered)
	}
	if _, left := mock.CRDs[inst.Namespace+"/"+k8s.MySQLClusterName(inst.ProjectID)]; left {
		t.Error("the MOCO cluster of the interrupted add was left behind")
	}
}

func TestAProjectWithoutADatabaseRefusesAMySQLVersion(t *testing.T) {
	svc, _, _ := setupMySQLTest(t)
	_, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName: "apps only", OrgID: "org1", OwnerID: testUser1, NoDatabase: true, MySQLVersion: "8.4.10",
	})
	if !errors.Is(err, ErrDatabaseSettingsWithoutDatabase) {
		t.Errorf("err = %v", err)
	}
}

// The feature gates creating MySQL projects, not looking after the ones that
// exist: turned off, they still pause, resume and delete.
func TestMySQLProjectsOutliveTheFeatureBeingTurnedOff(t *testing.T) {
	svc, store, mock := setupMySQLTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	inst := provisionMySQL(t, svc, store, "")
	svc.AllowMySQLCreation(false)
	if _, err := svc.AddDatabase(context.Background(), createWithoutDatabase(t, svc).ProjectID,
		domain.ProvisioningRequest{DBType: domain.MySQL}); err == nil {
		t.Error("a MySQL database was added with the feature off")
	}
	if err := svc.SetDeletionProtection(inst.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deprovision(context.Background(), inst.ProjectID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if _, left := mock.CRDs[inst.Namespace+"/"+k8s.MySQLClusterName(inst.ProjectID)]; left {
		t.Error("the cluster was left behind")
	}
}
