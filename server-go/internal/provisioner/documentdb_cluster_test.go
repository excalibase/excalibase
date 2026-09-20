package provisioner

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-409: the request's DocumentDB choice has to reach the cluster the
// provisioner applies, or the extension's libraries are never preloaded and
// the extension cannot be created however the project is recorded.

// documentDBKube is a fake cluster that reports every pod ready.
func documentDBKube() *k8s.MockClient {
	mock := k8s.NewMockClient()
	mock.WildcardPodReady = true
	mock.WildcardSecret = map[string][]byte{
		"username": []byte("app"),
		"password": []byte("testpassword123"),
		"dbname":   []byte("app"),
	}
	return mock
}

// appliedClusterPostgresql provisions once and returns the applied cluster's
// postgresql section.
func appliedClusterPostgresql(t *testing.T, documentDB bool) map[string]interface{} {
	t.Helper()
	mock := documentDBKube()
	majors := config.DocumentDBMajors()
	req := domain.ProvisioningRequest{
		ProjectName:     "docproj",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Free,
		PostgresVersion: majors[len(majors)-1],
		DocumentDB:      documentDB,
	}
	provisioners := NewPostgreSQLProvisioner(mock, "")
	if _, err := provisioners.Provision(context.Background(), req, config.TierConfig{Instances: 1, StorageSize: "5Gi"},
		func(domain.ProvisioningStage) {}); err != nil {
		t.Fatalf("provision: %v", err)
	}

	cluster, ok := mock.CRDs["org1-docproj/docproj-postgres"]
	if !ok {
		t.Fatalf("no cluster was applied: %v", mock.CRDs)
	}
	spec := cluster.Object["spec"].(map[string]interface{})
	return spec["postgresql"].(map[string]interface{})
}

func TestProvisionAppliesDocumentDBConfigurationToTheCluster(t *testing.T) {
	postgresql := appliedClusterPostgresql(t, true)

	libraries, _ := postgresql["shared_preload_libraries"].([]interface{})
	for _, want := range config.DocumentDBPreloadLibraries() {
		found := false
		for _, entry := range libraries {
			if entry == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the applied cluster does not preload %q: %v", want, libraries)
		}
	}
	params := postgresql["parameters"].(map[string]interface{})
	if _, set := params[config.DocumentDBCronDatabaseSetting]; !set {
		t.Errorf("the applied cluster does not set %s", config.DocumentDBCronDatabaseSetting)
	}
}

func TestProvisionAppliesNoDocumentDBConfigurationWhenItWasNotAsked(t *testing.T) {
	postgresql := appliedClusterPostgresql(t, false)

	libraries, _ := postgresql["shared_preload_libraries"].([]interface{})
	for _, entry := range libraries {
		for _, documentDBLibrary := range config.DocumentDBPreloadLibraries() {
			if entry == documentDBLibrary {
				t.Errorf("a plain project preloads %q", entry)
			}
		}
	}
	params := postgresql["parameters"].(map[string]interface{})
	if _, set := params[config.DocumentDBCronDatabaseSetting]; set {
		t.Errorf("a plain project sets %s", config.DocumentDBCronDatabaseSetting)
	}
}
