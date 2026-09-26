package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const testBackupSchedule = "0 0 3 * * *"

type fakeRestorePlans struct {
	plan  RestorePlan
	err   error
	asked []*domain.DatabaseInstance
}

func (f *fakeRestorePlans) RestorePlan(_ context.Context, src *domain.DatabaseInstance) (RestorePlan, error) {
	f.asked = append(f.asked, src)
	return f.plan, f.err
}

func enterprisePlan() *fakeRestorePlans {
	return &fakeRestorePlans{plan: RestorePlan{
		Tier:   domain.Enterprise,
		Config: config.TierConfig{Instances: 1, StorageSize: "500Gi", Memory: "16Gi", CPU: "4", StatementTimeout: "60s", BackupEnabled: true},
	}}
}

func backedUpEnterprisePlan() *fakeRestorePlans {
	plans := enterprisePlan()
	plans.plan.Backup = &domain.BackupSettings{Enabled: true, Schedule: testBackupSchedule, Retention: 14}
	return plans
}

func restoreDst(t *testing.T, adapter *K8sBackupAdapter, src *domain.DatabaseInstance) (*domain.ProvisioningResponse, error) {
	t.Helper()
	return adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
}

func restoredCluster(t *testing.T, mock *k8s.MockClient) *unstructured.Unstructured {
	t.Helper()
	cluster := mock.CRDs["org-dst/dst-postgres"]
	if cluster == nil {
		t.Fatalf("restore cluster not applied: %v", mock.CRDs)
	}
	return cluster
}

func tenantSource() *domain.DatabaseInstance {
	src := sourceInstance()
	src.StorageClass = "fast-ssd"
	src.Parameters = map[string]string{"work_mem": "64MB"}
	src.DatabaseName = "shop"
	src.Username = "owner"
	return src
}

func TestK8sRestoreRendersTheProjectsFullCluster(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	adapter := newRestoreReadyAdapter(t, mock, reg)
	plans := backedUpEnterprisePlan()
	adapter.SetRestorePlanSource(plans)
	src := tenantSource()

	if _, err := restoreDst(t, adapter, src); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got := restoredCluster(t, mock)
	want := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: "dst", Namespace: "org-dst", Tier: plans.plan.Config,
		StorageClass: "fast-ssd", Parameters: src.Parameters,
		DatabaseName: "shop", MasterUsername: "owner",
		ImageName: mustPostgresImage(t, src.PostgresVersion),
		Backup:    &k8s.BackupOpts{Schedule: testBackupSchedule, RetentionDays: 14, EndpointURL: testR2Endpoint, Bucket: "excalibase-backups"},
	})
	for _, field := range []string{"instances", "storage", "resources", "postgresql", "backup", "imageName", "monitoring"} {
		gotValue, _, _ := unstructured.NestedFieldNoCopy(got.Object, "spec", field)
		wantValue, _, _ := unstructured.NestedFieldNoCopy(want.Object, "spec", field)
		if !equalJSON(t, gotValue, wantValue) {
			t.Errorf("spec.%s: restored %v, a new project renders %v", field, gotValue, wantValue)
		}
	}
	if len(plans.asked) != 1 || plans.asked[0].ProjectID != "src" {
		t.Errorf("the plan must be resolved for the restore's source, asked %v", plans.asked)
	}
}

func TestK8sRestoreSchedulesBackupsAndRecordsTheProject(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	adapter := newRestoreReadyAdapter(t, mock, reg)
	adapter.SetRestorePlanSource(backedUpEnterprisePlan())

	if _, err := restoreDst(t, adapter, tenantSource()); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	scheduled := mock.CRDs["org-dst/dst-postgres-backup"]
	if scheduled == nil {
		t.Fatalf("no ScheduledBackup for the restored project: %v", mock.CRDs)
	}
	if schedule, _, _ := unstructured.NestedString(scheduled.Object, "spec", "schedule"); schedule != testBackupSchedule {
		t.Errorf("schedule = %q, want the plan's %q", schedule, testBackupSchedule)
	}
	if len(reg.calls) != 1 {
		t.Fatalf("registered %d projects, want 1", len(reg.calls))
	}
	row := reg.calls[0]
	if row.Tier != domain.Enterprise || row.StorageClass != "fast-ssd" || row.Parameters["work_mem"] != "64MB" {
		t.Errorf("the restored row must record its tier, storage class and parameters: %+v", row)
	}
	if row.BackupEnabled == nil || !*row.BackupEnabled || row.BackupSchedule != testBackupSchedule ||
		row.BackupRetentionDays == nil || *row.BackupRetentionDays != 14 {
		t.Errorf("the restored row must record its backups: %+v", row)
	}
}

func TestK8sRestoreWithoutBackupsRendersNone(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetRestorePlanSource(enterprisePlan())

	if _, err := restoreDst(t, adapter, tenantSource()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, has, _ := unstructured.NestedMap(restoredCluster(t, mock).Object, "spec", "backup"); has {
		t.Error("a plan without backups must render no backup section")
	}
	if _, scheduled := mock.CRDs["org-dst/dst-postgres-backup"]; scheduled {
		t.Error("a plan without backups must schedule none")
	}
}

func TestK8sRestoreRefusesABackupPlanWithoutASchedule(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	plans := backedUpEnterprisePlan()
	plans.plan.Backup.Schedule = ""
	adapter.SetRestorePlanSource(plans)

	if _, err := restoreDst(t, adapter, tenantSource()); !errors.Is(err, ErrRestoreBackupUnscheduled) {
		t.Fatalf("err: got %v, want ErrRestoreBackupUnscheduled", err)
	}
	if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 {
		t.Errorf("nothing may be created: ns=%v crds=%v", mock.Namespaces, mock.CRDs)
	}
}

func TestK8sRestoreRefusesWithoutAPlanSource(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetRestorePlanSource(nil)

	if _, err := restoreDst(t, adapter, sourceInstance()); !errors.Is(err, ErrRestorePlanNotConfigured) {
		t.Fatalf("err: got %v, want ErrRestorePlanNotConfigured", err)
	}
	if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 || len(mock.Secrets) != 0 {
		t.Errorf("nothing may be created without a plan: ns=%v crds=%v secrets=%v", mock.Namespaces, mock.CRDs, mock.Secrets)
	}
}

func TestK8sRestoreRefusesWhenThePlanCannotBeResolved(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	unresolved := errors.New("tier store unavailable")
	adapter.SetRestorePlanSource(&fakeRestorePlans{err: unresolved})

	if _, err := restoreDst(t, adapter, sourceInstance()); !errors.Is(err, unresolved) {
		t.Fatalf("err: got %v, want the plan resolution error", err)
	}
	if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 {
		t.Errorf("nothing may be created when the plan is unknown: ns=%v crds=%v", mock.Namespaces, mock.CRDs)
	}
}

func TestK8sRestoreRefusesATierThatDoesNotSizeTheCluster(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Free, Config: config.TierConfig{Instances: 1}}})

	if _, err := restoreDst(t, adapter, sourceInstance()); !errors.Is(err, k8s.ErrTierSizingIncomplete) {
		t.Fatalf("err: got %v, want ErrTierSizingIncomplete", err)
	}
	if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 {
		t.Errorf("nothing may be created for an unsized tier: ns=%v crds=%v", mock.Namespaces, mock.CRDs)
	}
}

func TestRestorePlanIsTheOrganisationsCurrentTier(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	setOrgTier(svc, "org", domain.Enterprise)
	src := sourceInstance()
	src.Tier = domain.Free

	plan, err := svc.RestorePlan(context.Background(), src)
	if err != nil {
		t.Fatalf("RestorePlan: %v", err)
	}
	want, _ := config.GetTierConfig(domain.Enterprise)
	if plan.Tier != domain.Enterprise || plan.Config != want {
		t.Errorf("got %s %+v, want the org's enterprise plan %+v", plan.Tier, plan.Config, want)
	}
}

func TestRestorePlanRefusesAnUnresolvableOrganisation(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	svc.orgStore.(*fakestore.Orgs).Err = errors.New("down")

	if _, err := svc.RestorePlan(context.Background(), sourceInstance()); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("err: got %v, want ErrOrgTierUnresolved", err)
	}
}

func TestRestorePlanRefusesWhenTheTierStoreFails(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	svc.SetTierStore(erroringTierStore{})

	if _, err := svc.RestorePlan(context.Background(), sourceInstance()); !errors.Is(err, ErrTierConfigUnavailable) {
		t.Fatalf("err: got %v, want ErrTierConfigUnavailable", err)
	}
}

func TestRestorePlanBacksUpExactlyAsANewProjectWould(t *testing.T) {
	cases := map[domain.TierType]bool{domain.Free: false, domain.Standard: true}
	for tier, wantBackups := range cases {
		t.Run(string(tier), func(t *testing.T) {
			svc, _, _ := setupProvisioningTest(t)
			svc.SetBackupDefaults(&BackupDefaults{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: testR2Endpoint, Bucket: "b", Schedule: testBackupSchedule, RetentionDays: 9})
			setOrgTier(svc, "org", tier)

			plan, err := svc.RestorePlan(context.Background(), sourceInstance())
			if err != nil {
				t.Fatalf("RestorePlan: %v", err)
			}
			if got := plan.Backup != nil && plan.Backup.Enabled; got != wantBackups {
				t.Fatalf("backups = %v, want %v for %s", got, wantBackups, tier)
			}
			if wantBackups && (plan.Backup.Schedule != testBackupSchedule || plan.Backup.Retention != 9) {
				t.Errorf("backup plan %+v must take the platform's schedule and retention", plan.Backup)
			}
		})
	}
}

func TestBackupServiceHandsThePlanSourceToItsAdapters(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetRestorePlanSource(nil)
	svc := NewBackupServiceWithAdapters(emptyInstanceStore(t), map[domain.DeploymentMode]BackupAdapter{domain.ModeK8s: adapter}, t.TempDir())

	plans := enterprisePlan()
	svc.SetRestorePlanSource(plans)

	if _, err := restoreDst(t, adapter, sourceInstance()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(plans.asked) != 1 {
		t.Error("the backup service must wire its plan source into the K8s adapter")
	}
}

func TestProvisionRecordsTheClusterSettingsARestoreNeeds(t *testing.T) {
	svc, store, _ := setupProvisioningTest(t)
	svc.SetStorageClassPolicy(config.StorageClassPolicy{Allowed: []string{"fast-ssd"}})
	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: "p", OrgID: "org", DBType: domain.PostgreSQL,
		StorageClass: "fast-ssd", Parameters: map[string]string{"work_mem": "64MB"},
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	inst, err := store.FindByProjectID(resp.ProjectID)
	if err != nil || inst == nil {
		t.Fatalf("find: %v", err)
	}
	if inst.StorageClass != "fast-ssd" || inst.Parameters["work_mem"] != "64MB" {
		t.Errorf("the project must record its storage class and parameters, got %q %v", inst.StorageClass, inst.Parameters)
	}
}

func mustPostgresImage(t *testing.T, major string) string {
	t.Helper()
	image, err := config.PostgresImage(major)
	if err != nil {
		t.Fatalf("image for %s: %v", major, err)
	}
	return image
}

func equalJSON(t *testing.T, a, b interface{}) bool {
	t.Helper()
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		t.Fatalf("encode: %v %v", errA, errB)
	}
	return string(left) == string(right)
}
