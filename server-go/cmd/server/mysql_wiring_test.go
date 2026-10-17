package main

import (
	"context"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// ADR 0041, EXC-583: on Kubernetes the MySQL provisioner is always there, so
// MySQL projects that exist are looked after; the mysql feature decides only
// whether new ones may be created.
func TestMySQLProvisionerAlwaysRegisteredOnKubernetes(t *testing.T) {
	for _, flags := range [][]features.Feature{nil, {features.MySQL}} {
		factory := buildK8sFactory(config.AppConfig{DarkFeatures: flags}, k8s.NewMockClient())
		if _, ok := factory.Get(domain.MySQL); !ok {
			t.Errorf("flags %v: no MySQL provisioner", flags)
		}
		if _, ok := factory.Get(domain.PostgreSQL); !ok {
			t.Errorf("flags %v: Postgres went missing", flags)
		}
	}
}

func TestMySQLCreationFollowsTheFeature(t *testing.T) {
	if mysqlCreationAllowed(config.AppConfig{}) {
		t.Error("MySQL creation is on with the feature off")
	}
	if !mysqlCreationAllowed(config.AppConfig{DarkFeatures: []features.Feature{features.MySQL}}) {
		t.Error("MySQL creation is off with the feature on")
	}
}

// Postgres keeps the deployment-mode pauser; MySQL pauses through its own,
// however the factory orders them.
func TestBuildPausersKeysPostgresByModeAndOthersByEngine(t *testing.T) {
	mock := k8s.NewMockClient()
	for i := 0; i < 20; i++ {
		factory := provisioner.NewFactory(provisioner.NewMySQLProvisioner(mock), provisioner.NewPostgreSQLProvisioner(mock, ""))
		byMode, byEngine := buildPausers(config.AppConfig{}, factory)
		if _, ok := byMode[domain.ModeK8s].(*provisioner.PostgreSQLProvisioner); !ok {
			t.Fatalf("the Kubernetes mode pauser is %T", byMode[domain.ModeK8s])
		}
		if _, ok := byEngine[domain.MySQL].(*provisioner.MySQLProvisioner); !ok {
			t.Fatalf("the MySQL pauser is %T", byEngine[domain.MySQL])
		}
	}
}

// EXC-589: the MySQL server certificate carries the public endpoint name, so
// a client dialling the public port verifies the name it dialled.
func TestMySQLCertificateNamesThePublicEndpoint(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.AutoReconcileClusters, mock.WildcardPodReady, mock.WildcardExecOutput = true, true, "1\n"
	factory := buildK8sFactory(config.AppConfig{DBEndpointDomain: "db.example.com"}, mock)
	prov, _ := factory.Get(domain.MySQL)
	req := domain.ProvisioningRequest{ProjectName: "proj-tls1", OrgID: "org1", DBType: domain.MySQL}
	if _, err := prov.Provision(context.Background(), req, config.TierConfig{Instances: 1, StorageSize: "5Gi", CPU: "0.5", Memory: "512Mi"},
		func(domain.ProvisioningStage) {}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	server := mock.CRDs["org1-proj-tls1/proj-tls1-mysql-server"]
	if server == nil {
		t.Fatal("no server certificate")
	}
	names, _, _ := unstructured.NestedStringSlice(server.Object, "spec", "dnsNames")
	if !slices.Contains(names, "proj-tls1.db.example.com") {
		t.Errorf("names %v", names)
	}
}
