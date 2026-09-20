package provisioner

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-409: the Mongo side of a DocumentDB project needs an identity of its
// own, and the sidecar injector reads it from a Secret in the project's
// namespace — unconditionally, with no "optional" on the env reference. A
// missing Secret therefore does not degrade the gateway, it stops the whole
// Postgres pod from starting. So the Secret is written before the cluster is
// applied, not after the pods are up.

// provisionForCredential runs one provision and returns the mock it ran against.
func provisionForCredential(t *testing.T, documentDB bool) (*k8s.MockClient, string) {
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
	if _, err := NewPostgreSQLProvisioner(mock, "").Provision(context.Background(), req,
		config.TierConfig{Instances: 1, StorageSize: "5Gi"}, func(domain.ProvisioningStage) {}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	return mock, "org1-docproj/" + k8s.DocumentDBCredentialSecretName("docproj")
}

func TestDocumentDBProvisionWritesTheGatewaysCredentialSecret(t *testing.T) {
	mock, key := provisionForCredential(t, true)

	secret, ok := mock.Secrets[key]
	if !ok {
		t.Fatalf("no credential Secret at %s: %v", key, mock.Secrets)
	}
	if got := string(secret["username"]); got != k8s.DocumentDBGatewayUsername {
		t.Errorf("username: got %q, want %q", got, k8s.DocumentDBGatewayUsername)
	}
	if len(secret["password"]) < 24 {
		t.Errorf("password is %d bytes; too short to be a credential", len(secret["password"]))
	}
}

// The Secret has to exist before the cluster does, because the gateway
// container's env references it and a pod whose env cannot be resolved never
// starts. Ordering this the other way round is the failure mode that would
// take a tenant's Postgres down, not merely their Mongo endpoint.
func TestDocumentDBCredentialSecretIsWrittenBeforeTheCluster(t *testing.T) {
	mock, _ := provisionForCredential(t, true)

	secretAt, clusterAt := -1, -1
	for i, call := range mock.Calls {
		if secretAt < 0 && strings.HasPrefix(call, "CreateSecret:") && strings.Contains(call, "documentdb-credentials") {
			secretAt = i
		}
		if clusterAt < 0 && strings.HasPrefix(call, "ApplyCRD:") {
			clusterAt = i
		}
	}
	if secretAt < 0 {
		t.Fatalf("the credential Secret was never created: %v", mock.Calls)
	}
	if clusterAt < 0 {
		t.Fatalf("no cluster was applied: %v", mock.Calls)
	}
	if secretAt > clusterAt {
		t.Errorf("the Secret was written after the cluster: %v", mock.Calls)
	}
}

// Two projects must not share a Mongo password, so it cannot be derived from
// anything about the project.
func TestDocumentDBCredentialPasswordDiffersBetweenProjects(t *testing.T) {
	first, firstKey := provisionForCredential(t, true)
	second, secondKey := provisionForCredential(t, true)

	if string(first.Secrets[firstKey]["password"]) == string(second.Secrets[secondKey]["password"]) {
		t.Error("two projects were given the same Mongo password")
	}
}

// A project without DocumentDB gets no such Secret: there is no gateway to
// read it and no Mongo identity to hold.
func TestAnOrdinaryProvisionWritesNoDocumentDBCredential(t *testing.T) {
	mock, key := provisionForCredential(t, false)

	if _, ok := mock.Secrets[key]; ok {
		t.Errorf("an ordinary project was given a Mongo credential at %s", key)
	}
}
