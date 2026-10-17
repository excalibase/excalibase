package provisioner

import (
	"context"
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
