package provisioner

import (
	"github.com/excalibase/provisioning-poc/internal/testutil/testca"

	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-589: the project's CA and server certificate exist before the cluster
// that mounts them, and the certificate names the public endpoint too.
func TestMySQLProvision_IssuesTheServerCertificateFirst(t *testing.T) {
	mock, prov := newMySQLTest(t)
	prov.SetPublicDomainSuffix("db.example.com")
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest(""), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	server, ok := mock.CRDs[mysqlTestNS+"/proj-my1-mysql-server"]
	if !ok {
		t.Fatalf("no server certificate; calls %v", mock.Calls)
	}
	names, _, _ := unstructured.NestedStringSlice(server.Object, "spec", "dnsNames")
	if !slices.Contains(names, "proj-my1.db.example.com") || !slices.Contains(names, k8s.MySQLPrimaryHost(mysqlTestProject, mysqlTestNS)) {
		t.Errorf("names %v", names)
	}
	certificate := slices.Index(mock.Calls, "ApplyCRD:"+mysqlTestNS+"/proj-my1-mysql-server")
	cluster := slices.Index(mock.Calls, "ApplyCRD:"+mysqlClusterKey)
	if certificate < 0 || cluster < 0 || certificate > cluster {
		t.Errorf("certificate applied at %d, cluster at %d", certificate, cluster)
	}
}

func TestMySQLServerCA(t *testing.T) {
	_, prov := newMySQLTest(t)
	got, err := prov.ServerCA(context.Background(), mysqlTestNS, mysqlTestProject)
	if err != nil || got != string(k8s.MockMySQLCA()["tls.crt"]) {
		t.Fatalf("got %q, %v", got, err)
	}
}

// cert-manager renews the file; mysqld reads it only at start or on
// ALTER INSTANCE RELOAD TLS, which leaves open sessions alone. Every
// instance reloads, replicas included.
func TestMySQLReloadTLS_ReloadsEveryInstance(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest(""), mysqlStandard, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	before := len(mock.Calls)
	if err := prov.ReloadTLS(context.Background(), mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatalf("ReloadTLS: %v", err)
	}
	reloaded := []string{}
	for _, call := range mock.Calls[before:] {
		if strings.HasPrefix(call, "ExecInPod:") {
			reloaded = append(reloaded, strings.TrimPrefix(call, "ExecInPod:"+mysqlTestNS+"/"))
		}
	}
	want := []string{"moco-proj-my1-mysql-0", "moco-proj-my1-mysql-1", "moco-proj-my1-mysql-2"}
	if !slices.Equal(reloaded, want) {
		t.Errorf("reloaded %v, want %v", reloaded, want)
	}
	if got := execsMatching(mock, "ALTER INSTANCE RELOAD TLS"); len(got) != 3 {
		t.Errorf("reload statements %v", got)
	}
}

// Removing the database removes its CA and certificate with it.
func TestMySQLRemoveDatabase_RemovesTheCertificates(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest(""), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	if err := prov.RemoveDatabase(context.Background(), mysqlTestNS, mysqlTestProject); err != nil {
		t.Fatalf("RemoveDatabase: %v", err)
	}
	for _, name := range []string{"proj-my1-mysql-server", "proj-my1-mysql-ca", "proj-my1-mysql-ca-issuer", "proj-my1-mysql-selfsigned"} {
		if _, ok := mock.CRDs[mysqlTestNS+"/"+name]; ok {
			t.Errorf("%s left behind", name)
		}
	}
	for _, secret := range []string{k8s.MySQLCASecretName(mysqlTestProject), k8s.MySQLServerTLSSecretName(mysqlTestProject)} {
		if !slices.Contains(mock.Calls, "DeleteSecret:"+mysqlTestNS+"/"+secret) {
			t.Errorf("secret %s left behind", secret)
		}
	}
}

// An instance that is not ready reads its certificate when it starts; one
// that refuses the reload is reported with its name.
func TestMySQLReloadTLS_SkipsUnreadyAndReportsFailures(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest(""), mysqlStandard, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	mock.PodReady[mysqlTestNS+"/moco-proj-my1-mysql-1"] = false
	mock.ExecError[mysqlTestNS+"/moco-proj-my1-mysql-2"] = errors.New("access denied")
	before := len(mock.Calls)

	err := prov.ReloadTLS(context.Background(), mysqlTestNS, mysqlTestProject)
	if err == nil || !strings.Contains(err.Error(), "moco-proj-my1-mysql-2") {
		t.Fatalf("err %v", err)
	}
	if slices.Contains(mock.Calls[before:], "ExecInPod:"+mysqlTestNS+"/moco-proj-my1-mysql-1") {
		t.Error("reloaded an instance that is not ready")
	}
}

func TestMySQLServerCA_MissingSecret(t *testing.T) {
	mock, prov := newMySQLTest(t)
	mock.NoClusterCA = true
	if _, err := prov.ServerCA(context.Background(), mysqlTestNS, mysqlTestProject); err == nil {
		t.Fatal("served a CA that does not exist")
	}
}

func TestMySQLRemoveDatabase_ReportsACertificateLeftBehind(t *testing.T) {
	mock, prov := newMySQLTest(t)
	mock.DeleteCRDError = errors.New("forbidden")
	if err := prov.removeTLS(context.Background(), mysqlTestNS, mysqlTestProject); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("err %v", err)
	}
}

// An instance whose readiness cannot be read is a failure of the sweep, not
// an instance to skip quietly.
func TestMySQLReloadTLS_ReportsAnUnreadableInstance(t *testing.T) {
	mock, prov := newMySQLTest(t)
	if _, err := prov.ProvisionWithRollback(context.Background(), mysqlRequest(""), mysqlFree, recordingContext()); err != nil {
		t.Fatalf("ProvisionWithRollback: %v", err)
	}
	mock.PodReadyError = errors.New("the API server timed out")
	err := prov.ReloadTLS(context.Background(), mysqlTestNS, mysqlTestProject)
	if err == nil || !strings.Contains(err.Error(), "moco-proj-my1-mysql-0") {
		t.Fatalf("err %v", err)
	}
}

// The sweep reads when the certificate cert-manager last issued runs out.
func TestMySQLServerCertificateExpiry(t *testing.T) {
	mock, prov := newMySQLTest(t)
	server := testca.New(t, "expiry").Issue(t, "db")
	mock.Secrets[mysqlTestNS+"/"+k8s.MySQLServerTLSSecretName(mysqlTestProject)] = map[string][]byte{"tls.crt": []byte(server.CertPEM)}
	got, err := prov.ServerCertificateExpiry(context.Background(), mysqlTestNS, mysqlTestProject)
	if err != nil || !got.Equal(server.Cert.NotAfter) {
		t.Fatalf("got %v, %v", got, err)
	}
}
