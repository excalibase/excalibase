package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// CloudNativePG applies the "<cluster>-app" Secret's password to the owner role
// whenever an instance manager starts as primary, so after a failover the
// owner holds whatever that Secret holds. A password the customer was given
// has to be in it too (EXC-759).

var errSecretDown = errors.New("api server unreachable")

const rotAppSecret = "org1-rot-db/rot-db-postgres-app"

func (h *rotationHarness) seedOwnerSecret(username, password string) {
	h.kube.Secrets[rotAppSecret] = map[string][]byte{
		"username": []byte(username), "password": []byte(password),
		"uri": []byte("postgresql://" + username + ":" + password + "@rot-db-postgres-rw:5432/app"),
	}
}

func TestResetFilesThePasswordInTheClusterOwnerSecret(t *testing.T) {
	h := oneTimeHarness(t)
	h.seedOwnerSecret(rotOwner, "generated-at-create")
	if _, err := h.svc.RotateCredentialsTo(context.Background(), rotProject, chosenOwnerPassword); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	secret := h.kube.Secrets[rotAppSecret]
	if string(secret["password"]) != chosenOwnerPassword {
		t.Fatalf("the cluster owner Secret still holds an old password; a failover would put it back")
	}
	if strings.Contains(string(secret["uri"]), "generated-at-create") {
		t.Errorf("the Secret's uri still carries the old password")
	}
}

func TestAResetThatCannotReachTheOwnerSecretFailsAndKeepsTheOldPassword(t *testing.T) {
	h := oneTimeHarness(t)
	h.seedOwnerSecret(rotOwner, "generated-at-create")
	h.kube.UpdateSecretError = errSecretDown
	before := h.currentOwnerPassword(t)
	if _, err := h.svc.RotateCredentialsTo(context.Background(), rotProject, chosenOwnerPassword); err == nil {
		t.Fatal("a reset that left the failover source stale was reported as done")
	}
	if h.currentOwnerPassword(t) != before {
		t.Errorf("the vault was promoted although the Secret was not updated")
	}
}

func TestResetRunsOnTheCurrentPrimaryNotPodOne(t *testing.T) {
	h := oneTimeHarness(t)
	h.seedOwnerSecret(rotOwner, "generated-at-create")
	cluster := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": rotProject + "-postgres", "namespace": rotNamespace},
		"status":   map[string]any{"currentPrimary": rotProject + "-postgres-2"},
	}}
	h.kube.CRDs[rotNamespace+"/"+rotProject+"-postgres"] = cluster
	h.kube.ExecOutput["org1-rot-db/rot-db-postgres-2"] = "ALTER ROLE"
	if _, err := h.svc.RotateCredentialsTo(context.Background(), rotProject, chosenOwnerPassword); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	for _, call := range h.kube.Calls {
		if call == "ExecInPod:"+rotPod {
			t.Fatalf("the password was altered on pod 1, a standby after the failover")
		}
	}
}

func TestACreatedProjectsOwnerSecretHoldsThePasswordItWasCreatedWith(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.AutoReconcileClusters = true
	svc.SetVault(newFakeVault())
	svc.SetFeatures(features.NewStatic(features.OneTimeCredentials))
	resp, err := svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: "chosen", OrgID: "org1", DBType: domain.PostgreSQL, OwnerPassword: chosenOwnerPassword,
	})
	if err != nil || resp.Status == "FAILED" {
		t.Fatalf("Provision: %v %+v", err, resp)
	}
	secret := mock.Secrets[resp.Namespace+"/"+resp.ProjectID+"-postgres-app"]
	if string(secret["password"]) != chosenOwnerPassword {
		t.Errorf("the cluster owner Secret holds a password other than the one the customer was given")
	}
}
