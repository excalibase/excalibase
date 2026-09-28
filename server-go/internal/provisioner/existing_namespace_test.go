package provisioner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-426: a database added to a project created without one goes into the
// namespace the project already has, beside its apps.

func addDatabaseRequest() domain.ProvisioningRequest {
	majors := config.DocumentDBMajors()
	return domain.ProvisioningRequest{
		ProjectName: "appsproj", OrgID: "org1", DBType: domain.PostgreSQL,
		PostgresVersion: majors[len(majors)-1], DocumentDB: true,
		IntoExistingNamespace: true,
	}
}

func calledWithPrefix(mock *k8s.MockClient, prefix string) bool {
	return slices.ContainsFunc(mock.Calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func TestAddingADatabaseUsesTheProjectsNamespace(t *testing.T) {
	mock := documentDBKube()
	mock.Namespaces["org1-appsproj"] = true
	pc := NewProvisionContext(nil, nil)

	result, err := NewPostgreSQLProvisioner(mock, "").ProvisionWithRollback(context.Background(),
		addDatabaseRequest(), config.TierConfig{Instances: 1, StorageSize: "5Gi"}, pc)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if calledWithPrefix(mock, "CreateProjectNamespace:") {
		t.Fatal("the namespace was created again")
	}
	if result.Namespace != "org1-appsproj" {
		t.Fatalf("namespace %q", result.Namespace)
	}
	if _, ok := mock.CRDs["org1-appsproj/appsproj-postgres"]; !ok {
		t.Fatal("no cluster was applied in the project's namespace")
	}

	// A later failure (registration) rolls back what was built, and only that.
	pc.Rollback(context.Background())
	if calledWithPrefix(mock, "DeleteNamespace:") {
		t.Fatal("rolling back an added database deleted the project's namespace and its apps")
	}
	if _, ok := mock.CRDs["org1-appsproj/appsproj-postgres"]; ok {
		t.Fatal("the cluster survived the rollback")
	}
	if _, ok := mock.Secrets["org1-appsproj/"+k8s.DocumentDBCredentialSecretName("appsproj")]; ok {
		t.Fatal("the DocumentDB credential survived the rollback, so a retry cannot create it")
	}
	if !calledWithPrefix(mock, "DeletePublicDBService:org1-appsproj/"+k8s.DocumentDBServiceName("appsproj")) {
		t.Fatal("the DocumentDB gateway Service was not removed")
	}
}

func TestAFailedDatabaseAddLeavesTheNamespace(t *testing.T) {
	mock := documentDBKube()
	mock.Namespaces["org1-appsproj"] = true
	mock.CRDError = errors.New("cluster rejected")
	pc := NewProvisionContext(nil, nil)

	_, err := NewPostgreSQLProvisioner(mock, "").ProvisionWithRollback(context.Background(),
		addDatabaseRequest(), config.TierConfig{Instances: 1, StorageSize: "5Gi"}, pc)
	if err == nil {
		t.Fatal("want the cluster failure reported")
	}
	pc.Rollback(context.Background())
	if calledWithPrefix(mock, "DeleteNamespace:") {
		t.Fatal("a failed database add deleted the project's namespace")
	}
	if _, ok := mock.Secrets["org1-appsproj/"+k8s.DocumentDBCredentialSecretName("appsproj")]; ok {
		t.Fatal("the DocumentDB credential survived the rollback")
	}
}
