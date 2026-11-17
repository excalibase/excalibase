package service

import (
	"context"
	"errors"
	"net/url"
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

func TestAResetThatCannotFileTheOwnerSecretLeavesVaultOnTheOldPasswordAndAPendingRecordToRetryFrom(t *testing.T) {
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
	// The role already has the new password; the pending record lets a retry converge.
	if len(h.pendingPaths(t)) == 0 {
		t.Errorf("no pending record was kept for the retry")
	}
	h.kube.UpdateSecretError = nil
	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if string(h.kube.Secrets[rotAppSecret]["password"]) != h.currentOwnerPassword(t) {
		t.Errorf("after the retry the Secret and the vault disagree")
	}
}

func TestAnOwnerSecretFiledUnderAnotherLoginFailsTheReset(t *testing.T) {
	h := oneTimeHarness(t)
	h.seedOwnerSecret("someone_else", "generated-at-create")
	if _, err := h.svc.RotateCredentialsTo(context.Background(), rotProject, chosenOwnerPassword); err == nil {
		t.Fatal("a Secret CNPG would restore from another login was skipped silently")
	}
}

const awkwardPassword = `p@ss:w/rd#?%&\x-long-enough`

func TestTheSecretsConnectionValuesSurviveAnAwkwardPassword(t *testing.T) {
	h := oneTimeHarness(t)
	oldPassword := "old:p@ss/w#rd"
	userinfo := url.UserPassword(rotOwner, oldPassword).String()
	h.kube.Secrets[rotAppSecret] = map[string][]byte{
		"username": []byte(rotOwner), "password": []byte(oldPassword), "dbname": []byte("app"),
		"uri":      []byte("postgresql://" + userinfo + "@rot-db-postgres-rw:5432/app"),
		"fqdn-uri": []byte("postgresql://" + userinfo + "@rot-db-postgres-rw.ns.svc.cluster.local:5432/app"),
		"jdbc-uri": []byte("jdbc:postgresql://rot-db-postgres-rw:5432/app?password=" + url.QueryEscape(oldPassword) + "&user=" + rotOwner),
		"pgpass":   []byte("rot-db-postgres-rw:5432:app:" + rotOwner + ":" + strings.NewReplacer(`\`, `\\`, ":", `\:`).Replace(oldPassword) + "\n"),
	}
	if _, err := h.svc.RotateCredentialsTo(context.Background(), rotProject, awkwardPassword); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	secret := h.kube.Secrets[rotAppSecret]
	for _, key := range []string{"uri", "fqdn-uri"} {
		parsed, err := url.Parse(string(secret[key]))
		if err != nil {
			t.Fatalf("%s does not parse: %v", key, err)
		}
		if got, _ := parsed.User.Password(); got != awkwardPassword || parsed.User.Username() != rotOwner {
			t.Errorf("%s does not carry the new password", key)
		}
	}
	for _, key := range []string{"jdbc-uri"} {
		parsed, err := url.Parse(strings.TrimPrefix(string(secret[key]), "jdbc:"))
		if err != nil || parsed.Query().Get("password") != awkwardPassword {
			t.Errorf("%s does not carry the new password (%v)", key, err)
		}
	}
	line := strings.TrimSuffix(string(secret["pgpass"]), "\n")
	fields := splitPgpass(line)
	if len(fields) != 5 || fields[4] != awkwardPassword {
		t.Errorf("pgpass does not carry the new password: %d fields", len(fields))
	}
}

// splitPgpass splits a .pgpass line on unescaped colons and unescapes it.
func splitPgpass(line string) []string {
	var fields []string
	var current strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line):
			i++
			current.WriteByte(line[i])
		case line[i] == ':':
			fields = append(fields, current.String())
			current.Reset()
		default:
			current.WriteByte(line[i])
		}
	}
	return append(fields, current.String())
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
