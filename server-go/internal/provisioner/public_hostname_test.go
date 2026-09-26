package provisioner

import (
	"context"
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func provisionedSpec(t *testing.T, domainSuffix string) map[string]interface{} {
	t.Helper()
	mock := documentDBKube()
	p := NewPostgreSQLProvisioner(mock, "")
	p.SetPublicDomainSuffix(domainSuffix)
	req := domain.ProvisioningRequest{
		ProjectName:     "docproj",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		PostgresVersion: config.PostgresMajors()[0],
	}
	if _, err := p.Provision(context.Background(), req, config.TierConfig{Instances: 1, StorageSize: "5Gi"},
		func(domain.ProvisioningStage) {}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	cluster, ok := mock.CRDs["org1-docproj/docproj-postgres"]
	if !ok {
		t.Fatalf("no cluster was applied: %v", mock.CRDs)
	}
	return cluster.Object["spec"].(map[string]interface{})
}

// The public connection string asks for sslmode=verify-full against the
// project's public name, so the server certificate has to carry that name.
func TestProvisionedClusterCertificateNamesThePublicHost(t *testing.T) {
	spec := provisionedSpec(t, "db.example.com")
	certificates, ok := spec["certificates"].(map[string]interface{})
	if !ok {
		t.Fatalf("no certificates section: %v", spec)
	}
	want := []interface{}{"docproj.db.example.com"}
	if got := certificates["serverAltDNSNames"]; !reflect.DeepEqual(got, want) {
		t.Errorf("serverAltDNSNames: got %v, want %v", got, want)
	}
}

func TestProvisionedClusterWithoutPublicDomainAddsNoName(t *testing.T) {
	spec := provisionedSpec(t, "")
	if certificates, ok := spec["certificates"]; ok {
		t.Errorf("certificates must be absent without a public domain, got %v", certificates)
	}
}

func TestProvisionRefusesAPublicHostThatIsNotAName(t *testing.T) {
	mock := documentDBKube()
	p := NewPostgreSQLProvisioner(mock, "")
	p.SetPublicDomainSuffix("localhost")
	req := domain.ProvisioningRequest{
		ProjectName:     "docproj",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		PostgresVersion: config.PostgresMajors()[0],
	}
	if _, err := p.Provision(context.Background(), req, config.TierConfig{Instances: 1, StorageSize: "5Gi"},
		func(domain.ProvisioningStage) {}); err == nil {
		t.Fatal("provisioned a cluster whose public name cannot be valid")
	}
	if _, ok := mock.CRDs["org1-docproj/docproj-postgres"]; ok {
		t.Error("the cluster was applied anyway")
	}
}
