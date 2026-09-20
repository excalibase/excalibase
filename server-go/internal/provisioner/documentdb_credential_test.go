package provisioner

import (
	"context"
	"errors"
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

// The Secret exists so the gateway container's env reference resolves, and
// holds nothing. A DocumentDB project has one credential — its own
// application role — and the gateway's entrypoint skips minting an admin user
// of its own when USERNAME and PASSWORD are empty. Putting a real password
// here would be a second identity, in a place nothing rotates.
func TestDocumentDBProvisionWritesAnEmptyCredentialSecret(t *testing.T) {
	mock, key := provisionForCredential(t, true)

	secret, ok := mock.Secrets[key]
	if !ok {
		t.Fatalf("no credential Secret at %s: %v", key, mock.Secrets)
	}
	for _, field := range []string{"username", "password"} {
		value, present := secret[field]
		if !present {
			t.Errorf("the Secret has no %s key at all; the gateway's env would not resolve", field)
		}
		if len(value) != 0 {
			t.Errorf("%s is set to %q; the gateway would mint a second identity", field, value)
		}
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

// A project without DocumentDB gets no such Secret: there is no gateway to
// read it and no Mongo identity to hold.
func TestAnOrdinaryProvisionWritesNoDocumentDBCredential(t *testing.T) {
	mock, key := provisionForCredential(t, false)

	if _, ok := mock.Secrets[key]; ok {
		t.Errorf("an ordinary project was given a Mongo credential at %s", key)
	}
}

// A cluster that will not take the Secret fails the provision. Carrying on
// would apply a cluster whose pods cannot start, and the failure would surface
// as an unexplained Postgres outage rather than as the provision that did not
// finish.
func TestDocumentDBProvisionFailsWhenTheCredentialCannotBeWritten(t *testing.T) {
	mock := documentDBKube()
	mock.CreateSecretError = errors.New("admission webhook denied the request")
	majors := config.DocumentDBMajors()

	_, err := NewPostgreSQLProvisioner(mock, "").Provision(context.Background(), domain.ProvisioningRequest{
		ProjectName:     "docproj",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Free,
		PostgresVersion: majors[len(majors)-1],
		DocumentDB:      true,
	}, config.TierConfig{Instances: 1, StorageSize: "5Gi"}, func(domain.ProvisioningStage) {})

	if err == nil {
		t.Fatal("the provision succeeded with no Mongo credential written")
	}
	if !strings.Contains(err.Error(), "documentdb") {
		t.Errorf("the failure does not say what could not be written: %v", err)
	}
	if _, applied := mock.CRDs["org1-docproj/docproj-postgres"]; applied {
		t.Error("a cluster was applied whose pods could not have started")
	}
}
