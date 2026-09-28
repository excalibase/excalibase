package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// setupClusterChangeTest is an ACTIVE FREE project in a FREE org whose
// cluster asks for 2Gi, below the plan's 5Gi, with one tenant setting.
func setupClusterChangeTest(t *testing.T) (*ProvisioningService, *storage.FileSystemStore, *k8s.MockClient) {
	t.Helper()
	svc, store, mock := setupOpsTest(t)
	svc.SetOrgStore(testOrgs())
	mock.Capacity = threeNodes()
	seedCluster(t, mock, config.TierConfig{Instances: 1, StorageSize: "2Gi", Memory: "512Mi", CPU: "0.5", StatementTimeout: "15s"},
		map[string]string{"work_mem": "4MB"})
	if err := store.UpdateStorageSizeIfStatus(testOpsDB, "2Gi", "ACTIVE"); err != nil {
		t.Fatalf("record disk: %v", err)
	}
	return svc, store, mock
}

func seedCluster(t *testing.T, mock *k8s.MockClient, tier config.TierConfig, params map[string]string) {
	t.Helper()
	cluster := k8s.BuildPostgreSQLCluster(k8s.PostgreSQLClusterOpts{
		ProjectID: testOpsDB, Namespace: testOpsDBNS, Tier: tier, Parameters: params,
	})
	if err := mock.ApplyCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, cluster); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
}

func opsCluster(t *testing.T, mock *k8s.MockClient) *unstructured.Unstructured {
	t.Helper()
	cluster, err := mock.GetCRD(context.Background(), k8s.CNPGClusterGVR, testOpsDBNS, testOpsDBPostgres)
	if err != nil {
		t.Fatalf("read cluster: %v", err)
	}
	return cluster.DeepCopy()
}

func clusterField(t *testing.T, cluster *unstructured.Unstructured, fields ...string) string {
	t.Helper()
	value, _, _ := unstructured.NestedFieldNoCopy(cluster.Object, append([]string{"spec"}, fields...)...)
	text, _ := value.(string)
	return text
}

func threeNodes() k8s.ClusterCapacity {
	nodes := make([]k8s.NodeCapacity, 3)
	for i := range nodes {
		nodes[i] = k8s.NodeCapacity{Name: "node", AllocatableCPUMilli: 8000, AllocatableMemBytes: 32 << 30}
	}
	return k8s.ClusterCapacity{Nodes: nodes}
}

type clusterChange struct {
	name string
	op   ProjectOperation
	run  func(*ProvisioningService) error
}

func clusterChanges() []clusterChange {
	ctx := context.Background()
	return []clusterChange{
		{"resize", OperationResize, func(s *ProvisioningService) error { return s.ResizeStorage(ctx, testOpsDB, "4Gi") }},
		{"tier", OperationTierChange, func(s *ProvisioningService) error { return s.ApplyOrgTier(ctx, testOpsDB, domain.Free) }},
		{"tune", OperationTune, func(s *ProvisioningService) error {
			return s.TuneParameters(ctx, testOpsDB, map[string]string{"work_mem": "8MB"})
		}},
	}
}

func TestClusterChangesTakeAndReleaseTheProjectLease(t *testing.T) {
	for _, tc := range clusterChanges() {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := setupClusterChangeTest(t)
			claimer := &countingClaimer{}
			svc.SetOperationClaimer(claimer)
			if err := tc.run(svc); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if claimer.claims != 1 || claimer.releases != 1 || claimer.heldNames[0] != tc.op {
				t.Fatalf("claims=%d releases=%d held=%v, want one %q lease given back", claimer.claims, claimer.releases, claimer.heldNames, tc.op)
			}
		})
	}
}

func TestClusterChangesAreRefusedWhileAnotherOperationHoldsTheProject(t *testing.T) {
	for _, tc := range clusterChanges() {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, mock := setupClusterChangeTest(t)
			svc.SetOperationClaimer(&heldClaimer{})
			before := opsCluster(t, mock)
			err := tc.run(svc)
			if !errors.Is(err, ErrProjectOperationRunning) || !errors.Is(err, storage.ErrProjectBusy) {
				t.Fatalf("err = %v, want the project-busy refusal", err)
			}
			if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
				t.Error("the cluster changed while another operation held the project")
			}
			if inst, _ := store.FindByProjectID(testOpsDB); len(inst.Parameters) != 0 || inst.Tier != domain.Free {
				t.Error("the project row changed while another operation held the project")
			}
		})
	}
}

// PENDING_DELETION is the EXC-473 grace period: stopped, and not to be grown.
func TestClusterChangesNeedAnActiveProject(t *testing.T) {
	statuses := []string{string(domain.StatusPaused), string(domain.StatusPendingDeletion), string(domain.StatusDeleting), string(domain.StatusRestoring), "FAILED"}
	for _, tc := range clusterChanges() {
		for _, status := range statuses {
			t.Run(tc.name+"/"+status, func(t *testing.T) {
				svc, store, mock := setupClusterChangeTest(t)
				markStatus(t, store, status)
				before := opsCluster(t, mock)
				err := tc.run(svc)
				if !errors.Is(err, ErrProjectNotActive) {
					t.Fatalf("err = %v, want ErrProjectNotActive", err)
				}
				if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
					t.Errorf("the cluster of a %s project was changed", status)
				}
			})
		}
	}
}

func TestClusterChangesAreForKubernetesProjectsOnly(t *testing.T) {
	for _, tc := range clusterChanges() {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, _ := setupClusterChangeTest(t)
			inst, _ := store.FindByProjectID(testOpsDB)
			inst.DeploymentMode = domain.ModeDocker
			if err := store.Update(inst); err != nil {
				t.Fatalf("mark docker: %v", err)
			}
			if err := tc.run(svc); !errors.Is(err, ErrKubernetesOnly) {
				t.Fatalf("err = %v, want ErrKubernetesOnly", err)
			}
		})
	}
}

func TestResizeStorageGrowsTheClustersVolume(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	if err := svc.ResizeStorage(context.Background(), testOpsDB, "5Gi"); err != nil {
		t.Fatalf("ResizeStorage: %v", err)
	}
	if got := clusterField(t, opsCluster(t, mock), "storage", "size"); got != "5Gi" {
		t.Errorf("storage size = %q, want 5Gi", got)
	}
}

func TestResizeStorageRefusals(t *testing.T) {
	cases := []struct {
		name, size string
		want       error
	}{
		{"shrink", "1Gi", ErrStorageShrink},
		{"same size", "2Gi", ErrStorageShrink},
		{"above the plan", "6Gi", ErrStorageAbovePlan},
		{"not whole gibibytes", "2500Mi", ErrInvalidStorageSize},
		{"decimal units", "3GB", ErrInvalidStorageSize},
		{"zero", "0Gi", ErrInvalidStorageSize},
		{"negative", "-3Gi", ErrInvalidStorageSize},
		{"empty", "", ErrInvalidStorageSize},
		{"leading zero", "03Gi", ErrInvalidStorageSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, mock := setupClusterChangeTest(t)
			before := opsCluster(t, mock)
			if err := svc.ResizeStorage(context.Background(), testOpsDB, tc.size); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
				t.Error("a refused resize changed the cluster")
			}
		})
	}
}

// A volume CloudNativePG could not grow would leave the cluster asking for a
// size it can never have, and the spec cannot be shrunk back.
func TestResizeStorageIsRefusedWhenTheVolumesCannotGrow(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	mock.VolumeExpansionError = k8s.ErrVolumeExpansionUnsupported
	before := opsCluster(t, mock)
	if err := svc.ResizeStorage(context.Background(), testOpsDB, "4Gi"); !errors.Is(err, k8s.ErrVolumeExpansionUnsupported) {
		t.Fatalf("err = %v, want ErrVolumeExpansionUnsupported", err)
	}
	if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
		t.Error("the cluster changed although its volumes cannot grow")
	}
}

func TestTuneParametersAppliesAndRecordsTheTenantSettings(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	want := map[string]string{"work_mem": "8MB", "jit": "off"}
	if err := svc.TuneParameters(context.Background(), testOpsDB, want); err != nil {
		t.Fatalf("TuneParameters: %v", err)
	}
	cluster := opsCluster(t, mock)
	if got := k8s.TenantParameters(cluster); !maps.Equal(got, want) {
		t.Errorf("cluster tenant parameters = %v, want %v", got, want)
	}
	if clusterField(t, cluster, "postgresql", "parameters", "statement_timeout") != "15s" {
		t.Error("the tier's statement guard was lost")
	}
	if inst, _ := store.FindByProjectID(testOpsDB); !maps.Equal(inst.Parameters, want) {
		t.Errorf("recorded parameters = %v, want %v", inst.Parameters, want)
	}
}

func TestTuneParametersWithNoneResetsToTheDefaults(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	if err := svc.TuneParameters(context.Background(), testOpsDB, map[string]string{}); err != nil {
		t.Fatalf("TuneParameters: %v", err)
	}
	if got := k8s.TenantParameters(opsCluster(t, mock)); len(got) != 0 {
		t.Errorf("tenant parameters = %v, want none", got)
	}
}

func TestTuneParametersRefusesWhatIsNotTheTenants(t *testing.T) {
	cases := map[string]map[string]string{
		"superuser-only library":    {"shared_preload_libraries": "pg_stat_statements"},
		"archive command":           {"archive_command": "curl evil"},
		"the tier's timeout":        {"statement_timeout": "0"},
		"connection limit":          {"max_connections": "1000"},
		"above the tier's memory":   {"work_mem": "64MB"},
		"one bad among good":        {"jit": "off", "log_min_duration_statement": "0"},
		"not a value it takes":      {"jit": "maybe"},
		"file-reading setting":      {"ssl_key_file": "/etc/passwd"},
		"a library loaded per role": {"session_preload_libraries": "x"},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			svc, store, mock := setupClusterChangeTest(t)
			before := opsCluster(t, mock)
			if err := svc.TuneParameters(context.Background(), testOpsDB, params); !errors.Is(err, config.ErrTenantParameter) {
				t.Fatalf("err = %v, want ErrTenantParameter", err)
			}
			if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
				t.Error("a refused tuning changed the cluster")
			}
			if inst, _ := store.FindByProjectID(testOpsDB); len(inst.Parameters) != 0 {
				t.Error("a refused tuning was recorded")
			}
		})
	}
}

// failingParameterStore records nothing, so the cluster must be put back.
type failingParameterStore struct{ *storage.FileSystemStore }

func (failingParameterStore) UpdateParametersIfStatus(string, map[string]string, string) error {
	return errors.New("platform database unavailable")
}

func TestTuneParametersPutsTheClusterBackWhenTheRecordFails(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	svc.store = failingParameterStore{store}
	if err := svc.TuneParameters(context.Background(), testOpsDB, map[string]string{"work_mem": "8MB"}); err == nil {
		t.Fatal("a tuning that could not be recorded reported success")
	}
	if got := k8s.TenantParameters(opsCluster(t, mock)); got["work_mem"] != "4MB" {
		t.Errorf("tenant parameters = %v, want the previous work_mem=4MB back", got)
	}
}

func TestApplyOrgTierRefusesATierThatIsNotTheOrgsPlan(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	before := opsCluster(t, mock)
	err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Enterprise)
	if !errors.Is(err, ErrTierNotOrgPlan) || !strings.Contains(err.Error(), string(domain.Free)) {
		t.Fatalf("err = %v, want ErrTierNotOrgPlan naming the org's plan", err)
	}
	if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
		t.Error("the cluster changed")
	}
	if inst, _ := store.FindByProjectID(testOpsDB); inst.Tier != domain.Free {
		t.Error("the project's tier changed")
	}
}

func TestApplyOrgTierMovesTheClusterOntoTheOrgsPlan(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	mock.Capacity = threeNodes()
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Standard); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
	cluster := opsCluster(t, mock)
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	if instances != 3 || clusterField(t, cluster, "resources", "limits", "memory") != "4Gi" ||
		clusterField(t, cluster, "storage", "size") != "50Gi" ||
		clusterField(t, cluster, "postgresql", "parameters", "statement_timeout") != "30s" {
		t.Errorf("cluster not on the STANDARD plan: %v", cluster.Object["spec"])
	}
	if k8s.TenantParameters(cluster)["work_mem"] != "4MB" {
		t.Error("the tenant's settings were lost")
	}
	if inst, _ := store.FindByProjectID(testOpsDB); inst.Tier != domain.Standard {
		t.Errorf("project tier = %s, want STANDARD", inst.Tier)
	}
}

func TestApplyOrgTierRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*ProvisioningService, *k8s.MockClient, *testing.T)
		want  func(error) bool
	}{
		{"fewer nodes than instances", func(s *ProvisioningService, m *k8s.MockClient, _ *testing.T) {
			setOrgTier(s, "org1", domain.Standard)
			m.Capacity = k8s.ClusterCapacity{Nodes: threeNodes().Nodes[:1]}
		}, func(err error) bool { var nodes *NotEnoughNodesError; return errors.As(err, &nodes) }},
		{"a larger disk its volumes cannot grow to", func(s *ProvisioningService, m *k8s.MockClient, _ *testing.T) {
			setOrgTier(s, "org1", domain.Standard)
			m.Capacity = threeNodes()
			m.VolumeExpansionError = k8s.ErrVolumeExpansionUnsupported
		}, func(err error) bool { return errors.Is(err, k8s.ErrVolumeExpansionUnsupported) }},
		{"a disk larger than the plan's maximum", func(_ *ProvisioningService, m *k8s.MockClient, t *testing.T) {
			seedCluster(t, m, config.TierConfig{Instances: 1, StorageSize: "50Gi", Memory: "512Mi", CPU: "0.5"}, nil)
		}, func(err error) bool { return errors.Is(err, ErrDiskAbovePlanMax) }},
		{"tenant settings beyond the new plan's bounds", func(_ *ProvisioningService, m *k8s.MockClient, t *testing.T) {
			seedCluster(t, m, config.TierConfig{Instances: 1, StorageSize: "2Gi", Memory: "512Mi", CPU: "0.5"},
				map[string]string{"work_mem": "256MB"})
		}, func(err error) bool { return errors.Is(err, ErrTierParametersOutOfBounds) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, mock := setupClusterChangeTest(t)
			tc.setup(svc, mock, t)
			before := opsCluster(t, mock)
			org, _ := svc.orgTier(context.Background(), "org1")
			err := svc.ApplyOrgTier(context.Background(), testOpsDB, org)
			if !tc.want(err) {
				t.Fatalf("err = %v", err)
			}
			if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
				t.Error("a refused tier change changed the cluster")
			}
			if inst, _ := store.FindByProjectID(testOpsDB); inst.Tier != domain.Free {
				t.Error("a refused tier change changed the project's tier")
			}
		})
	}
}

func TestClusterSettingsReportTheClusterAndThePlan(t *testing.T) {
	svc, _, _ := setupClusterChangeTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	got, err := svc.ClusterSettings(context.Background(), testOpsDB)
	if err != nil {
		t.Fatalf("ClusterSettings: %v", err)
	}
	if got.Tier != domain.Free || got.OrgTier != domain.Standard || got.StorageSize != "2Gi" || got.StorageStart != "5Gi" || got.StorageLimit != "5Gi" ||
		got.Instances != 1 || got.Memory != "512Mi" || got.CPU != "0.5" || got.Parameters["work_mem"] != "4MB" {
		t.Errorf("settings = %+v", got)
	}
	if len(got.TunableParameters) == 0 || !config.TenantTunableParameter(got.TunableParameters[0]) {
		t.Errorf("tunable parameters = %v", got.TunableParameters)
	}
}

// fullNodes are three nodes with no CPU or memory left for anything.
func fullNodes() k8s.ClusterCapacity {
	capacity := threeNodes()
	for i := range capacity.Nodes {
		capacity.Nodes[i].Name = fmt.Sprintf("n%d", i+1)
		capacity.Nodes[i].RequestedCPUMilli = capacity.Nodes[i].AllocatableCPUMilli
		capacity.Nodes[i].RequestedMemBytes = capacity.Nodes[i].AllocatableMemBytes
	}
	return capacity
}

// A bigger plan that no node has room for would leave the restarted instance
// Pending: the database would be down, not resized.
func TestApplyOrgTierIsRefusedWhenNoNodeHasRoomForThePlan(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	seedCluster(t, mock, config.TierConfig{Instances: 1, StorageSize: "2Gi", Memory: "256Mi", CPU: "0.25"}, nil)
	mock.Capacity = fullNodes()
	before := opsCluster(t, mock)
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Free); !errors.Is(err, ErrPlanDoesNotFit) {
		t.Fatalf("err = %v, want ErrPlanDoesNotFit", err)
	}
	if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
		t.Error("the cluster changed although the plan does not fit")
	}
	if inst, _ := store.FindByProjectID(testOpsDB); inst.Tier != domain.Free {
		t.Error("the project's tier changed")
	}
}

// The node an instance already runs on only needs room for the growth: its
// current requests are already counted there.
func TestApplyOrgTierCountsOnlyTheGrowthOnTheInstancesOwnNode(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	seedCluster(t, mock, config.TierConfig{Instances: 1, StorageSize: "2Gi", Memory: "256Mi", CPU: "0.25"}, nil)
	capacity := fullNodes()
	capacity.Nodes[1].RequestedCPUMilli -= 300
	capacity.Nodes[1].RequestedMemBytes -= 300 << 20
	mock.Capacity = capacity
	mock.Pods[testOpsDBNS] = []corev1.Pod{{Spec: corev1.PodSpec{NodeName: "n2"}}}
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Free); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
	if got := clusterField(t, opsCluster(t, mock), "resources", "requests", "memory"); got != "512Mi" {
		t.Errorf("memory request = %q, want the FREE plan's 512Mi", got)
	}
}

// A plan no larger than what runs needs no new room, even on a full node.
func TestApplyOrgTierToASmallerSizeNeedsNoRoom(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	seedCluster(t, mock, config.TierConfig{Instances: 1, StorageSize: "2Gi", Memory: "1Gi", CPU: "1"}, nil)
	mock.Capacity = fullNodes()
	mock.Pods[testOpsDBNS] = []corev1.Pod{{Spec: corev1.PodSpec{NodeName: "n1"}}}
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Free); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
}

// failingStatusStore refuses the row write a tier change ends with.
type failingStatusStore struct{ *storage.FileSystemStore }

func (failingStatusStore) UpdateIfStatus(*domain.DatabaseInstance, string) error {
	return errors.New("platform database unavailable")
}

// The row's tier bounds tuning and resizing, so a cluster left on a plan the
// row does not record would be tuned against the wrong plan.
func TestApplyOrgTierPutsTheSizeBackWhenTheRecordFails(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	svc.store = failingStatusStore{store}
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Standard); err == nil {
		t.Fatal("a tier change that could not be recorded reported success")
	}
	cluster := opsCluster(t, mock)
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	if instances != 1 || clusterField(t, cluster, "resources", "limits", "memory") != "512Mi" ||
		clusterField(t, cluster, "postgresql", "parameters", "statement_timeout") != "15s" {
		t.Errorf("cluster left on the unrecorded plan: %v", cluster.Object["spec"])
	}
}

// A cluster API failure is an error, never a change reported as done.
func TestClusterChangesReportAClusterThatCannotBeWritten(t *testing.T) {
	for _, tc := range clusterChanges() {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, mock := setupClusterChangeTest(t)
			mock.UpdateCRDError = errors.New("apiserver unavailable")
			if err := tc.run(svc); err == nil {
				t.Fatal("a change the cluster did not take reported success")
			}
			if inst, _ := store.FindByProjectID(testOpsDB); len(inst.Parameters) != 0 {
				t.Error("a change the cluster did not take was recorded")
			}
		})
	}
}

func TestClusterChangesReportAMissingCluster(t *testing.T) {
	for _, tc := range clusterChanges() {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, mock := setupClusterChangeTest(t)
			delete(mock.CRDs, testOpsDBNS+"/"+testOpsDBPostgres)
			if err := tc.run(svc); err == nil {
				t.Fatal("a change on a missing cluster reported success")
			}
		})
	}
	svc, _, mock := setupClusterChangeTest(t)
	delete(mock.CRDs, testOpsDBNS+"/"+testOpsDBPostgres)
	if _, err := svc.ClusterSettings(context.Background(), testOpsDB); err == nil {
		t.Error("settings of a missing cluster were answered")
	}
}

func TestClusterSettingsAreForKubernetesProjectsOnly(t *testing.T) {
	svc, store, _ := setupClusterChangeTest(t)
	inst, _ := store.FindByProjectID(testOpsDB)
	inst.DeploymentMode = domain.ModeDocker
	if err := store.Update(inst); err != nil {
		t.Fatalf("mark docker: %v", err)
	}
	if _, err := svc.ClusterSettings(context.Background(), testOpsDB); !errors.Is(err, ErrKubernetesOnly) {
		t.Fatalf("err = %v, want ErrKubernetesOnly", err)
	}
	if _, err := svc.ClusterSettings(context.Background(), "missing"); err == nil {
		t.Error("settings of a missing project were answered")
	}
}

// The org's plan decides the tier; an unreadable one refuses, never defaults.
func TestApplyOrgTierRefusesWhenTheOrgsPlanCannotBeRead(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	svc.SetOrgStore(nil)
	before := opsCluster(t, mock)
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Free); !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("err = %v, want ErrOrgTierUnresolved", err)
	}
	if !equalUnstructured(before.Object, opsCluster(t, mock).Object) {
		t.Error("the cluster changed")
	}
}

func TestApplyOrgTierRefusesWhenTheNodesCannotBeRead(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	mock.GetPodsError = errors.New("apiserver unavailable")
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Free); !errors.Is(err, ErrNodePlacementUnknown) {
		t.Fatalf("err = %v, want ErrNodePlacementUnknown", err)
	}
	mock.GetPodsError = nil
	mock.CapacityError = errors.New("apiserver unavailable")
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Free); !errors.Is(err, ErrNodePlacementUnknown) {
		t.Fatalf("err = %v, want ErrNodePlacementUnknown", err)
	}
}

func TestQuantitiesRefuseWhatIsNotAQuantity(t *testing.T) {
	if _, _, err := quantities("lots", "1Gi"); err == nil {
		t.Error("a CPU that is not a quantity was read")
	}
	if _, _, err := quantities("1", "lots"); err == nil {
		t.Error("a memory that is not a quantity was read")
	}
}

// setStandardProject puts the project on STANDARD in a STANDARD org with a
// cluster of the given disk.
func setStandardProject(t *testing.T, svc *ProvisioningService, store *storage.FileSystemStore, mock *k8s.MockClient, disk string) {
	t.Helper()
	setOrgTier(svc, "org1", domain.Standard)
	inst, _ := store.FindByProjectID(testOpsDB)
	inst.Tier = domain.Standard
	if err := store.Update(inst); err != nil {
		t.Fatalf("mark standard: %v", err)
	}
	seedCluster(t, mock, config.TierConfig{Instances: 3, StorageSize: disk, Memory: "4Gi", CPU: "2", StatementTimeout: "30s"}, nil)
}

// A plan's disk may grow past where it starts, up to the plan's maximum.
func TestResizeStorageGrowsUpToThePlansMaximum(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	setStandardProject(t, svc, store, mock, "50Gi")
	if err := svc.ResizeStorage(context.Background(), testOpsDB, "500Gi"); err != nil {
		t.Fatalf("ResizeStorage to the maximum: %v", err)
	}
	if got := clusterField(t, opsCluster(t, mock), "storage", "size"); got != "500Gi" {
		t.Errorf("storage = %q, want 500Gi", got)
	}
	err := svc.ResizeStorage(context.Background(), testOpsDB, "501Gi")
	if !errors.Is(err, ErrStorageAbovePlan) || !strings.Contains(err.Error(), "500Gi") {
		t.Fatalf("err = %v, want ErrStorageAbovePlan naming 500Gi", err)
	}
}

// Free's disk is fixed: its maximum is the disk it starts with.
func TestResizeStorageOnFreeIsRefusedAtItsFixedDisk(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	seedCluster(t, mock, config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"}, nil)
	if err := svc.ResizeStorage(context.Background(), testOpsDB, "6Gi"); !errors.Is(err, ErrStorageAbovePlan) {
		t.Fatalf("err = %v, want ErrStorageAbovePlan", err)
	}
}

// A disk grown past the plan's start is kept on a plan change that allows it.
func TestApplyOrgTierKeepsAGrownDiskWithinThePlan(t *testing.T) {
	svc, store, mock := setupClusterChangeTest(t)
	setStandardProject(t, svc, store, mock, "80Gi")
	if err := svc.ApplyOrgTier(context.Background(), testOpsDB, domain.Standard); err != nil {
		t.Fatalf("ApplyOrgTier: %v", err)
	}
	if got := clusterField(t, opsCluster(t, mock), "storage", "size"); got != "80Gi" {
		t.Errorf("storage = %q, want the grown 80Gi kept", got)
	}
}

func TestClusterSettingsReportTheDiskTheDatabasesUse(t *testing.T) {
	svc, _, mock := setupClusterChangeTest(t)
	cluster := opsCluster(t, mock)
	_ = unstructured.SetNestedField(cluster.Object, testOpsDBPostgres+"-1", "status", "currentPrimary")
	mock.CRDs[testOpsDBNS+"/"+testOpsDBPostgres] = cluster
	mock.ExecOutput[testOpsDBNS+"/"+testOpsDBPostgres+"-1"] = "123456789\n"
	got, err := svc.ClusterSettings(context.Background(), testOpsDB)
	if err != nil {
		t.Fatalf("ClusterSettings: %v", err)
	}
	if got.StorageUsedBytes == nil || *got.StorageUsedBytes != 123456789 {
		t.Errorf("used = %v, want 123456789", got.StorageUsedBytes)
	}
}

// Usage is shown, never guessed: an unreadable size is reported as unknown.
func TestClusterSettingsReportUnknownUsageAsAbsent(t *testing.T) {
	svc, _, _ := setupClusterChangeTest(t)
	got, err := svc.ClusterSettings(context.Background(), testOpsDB)
	if err != nil {
		t.Fatalf("ClusterSettings: %v", err)
	}
	if got.StorageUsedBytes != nil {
		t.Errorf("used = %d, want unknown", *got.StorageUsedBytes)
	}
}
