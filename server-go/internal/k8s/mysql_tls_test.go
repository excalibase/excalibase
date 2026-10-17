package k8s

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func tlsObject(t *testing.T, objects []MySQLTLSObject, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, object := range objects {
		if object.Object.GetKind() == kind && object.Object.GetName() == name {
			return object.Object
		}
	}
	t.Fatalf("no %s %s in %v", kind, name, objects)
	return nil
}

// EXC-589: a project's MySQL server certificate comes from its own
// cert-manager CA, which keeps its key across renewals so a client holding
// the CA keeps verifying, and names every address a client dials.
func TestBuildMySQLTLS(t *testing.T) {
	opts := mysqlOpts(freePlan)
	opts.PublicNames = []string{"proj-abc.db.example.com"}
	objects := BuildMySQLTLS(opts)
	assertMySQLTLSOrder(t, objects)
	assertMySQLCA(t, objects)
	assertMySQLServerCertificate(t, objects)
}

func assertMySQLTLSOrder(t *testing.T, objects []MySQLTLSObject) {
	t.Helper()
	order := make([]string, 0, len(objects))
	for _, object := range objects {
		if object.Object.GetNamespace() != "org-proj-abc" || object.Object.GetLabels()["excalibase.io/project"] != "proj-abc" {
			t.Errorf("%s %s: namespace %q labels %v", object.Object.GetKind(), object.Object.GetName(), object.Object.GetNamespace(), object.Object.GetLabels())
		}
		order = append(order, object.Object.GetKind()+"/"+object.Object.GetName())
	}
	want := []string{"Issuer/proj-abc-mysql-selfsigned", "Certificate/proj-abc-mysql-ca", "Issuer/proj-abc-mysql-ca-issuer", "Certificate/proj-abc-mysql-server"}
	if !slices.Equal(order, want) {
		t.Fatalf("objects %v, want %v", order, want)
	}
}

func assertMySQLCA(t *testing.T, objects []MySQLTLSObject) {
	t.Helper()
	ca := tlsObject(t, objects, "Certificate", "proj-abc-mysql-ca")
	if isCA, _, _ := unstructured.NestedBool(ca.Object, "spec", "isCA"); !isCA {
		t.Error("the CA certificate is not a CA")
	}
	if policy, _, _ := unstructured.NestedString(ca.Object, "spec", "privateKey", "rotationPolicy"); policy != "Never" {
		t.Errorf("CA key rotation %q: a renewed CA with a new key would strand every client holding the old one", policy)
	}
	if secret, _, _ := unstructured.NestedString(ca.Object, "spec", "secretName"); secret != MySQLCASecretName("proj-abc") {
		t.Errorf("CA secret %q", secret)
	}
	if issuer, _, _ := unstructured.NestedString(tlsObject(t, objects, "Issuer", "proj-abc-mysql-ca-issuer").Object, "spec", "ca", "secretName"); issuer != MySQLCASecretName("proj-abc") {
		t.Errorf("CA issuer signs with %q", issuer)
	}
}

func assertMySQLServerCertificate(t *testing.T, objects []MySQLTLSObject) {
	t.Helper()
	server := tlsObject(t, objects, "Certificate", "proj-abc-mysql-server")
	for field, want := range map[string]string{
		"secretName": MySQLServerTLSSecretName("proj-abc"), "duration": "2160h", "renewBefore": "720h",
	} {
		if got, _, _ := unstructured.NestedString(server.Object, "spec", field); got != want {
			t.Errorf("server %s %q, want %q", field, got, want)
		}
	}
	if issuer, _, _ := unstructured.NestedString(server.Object, "spec", "issuerRef", "name"); issuer != "proj-abc-mysql-ca-issuer" {
		t.Errorf("server issuer %q", issuer)
	}
	names, _, _ := unstructured.NestedStringSlice(server.Object, "spec", "dnsNames")
	for _, name := range []string{
		MySQLPrimaryHost("proj-abc", "org-proj-abc"), MySQLReplicaHost("proj-abc", "org-proj-abc"),
		"moco-proj-abc-mysql-primary.org-proj-abc.svc", "moco-proj-abc-mysql-primary",
		"*.moco-proj-abc-mysql.org-proj-abc.svc.cluster.local", "proj-abc.db.example.com",
	} {
		if !slices.Contains(names, name) {
			t.Errorf("server certificate lacks %s: %v", name, names)
		}
	}
}

// cert-manager refuses a commonName over 64 bytes, and a real project's
// namespace is a 32-hex org id plus the project id, so the Service name never
// fits there: the names a client verifies live in dnsNames only.
func TestMySQLTLSCommonNamesFitCertManagerLimit(t *testing.T) {
	opts := mysqlOpts(freePlan)
	opts.ProjectID = "proj-so9odu5s4g"
	opts.Namespace = "983442e2d6488cf64859fd451b977e96-proj-so9odu5s4g"
	for _, object := range BuildMySQLTLS(opts) {
		if object.Object.GetKind() != "Certificate" {
			continue
		}
		if cn, _, _ := unstructured.NestedString(object.Object.Object, "spec", "commonName"); len(cn) > 64 {
			t.Errorf("%s commonName is %d bytes, cert-manager allows 64: %q", object.Object.GetName(), len(cn), cn)
		}
	}
	server := tlsObject(t, BuildMySQLTLS(opts), "Certificate", "proj-so9odu5s4g-mysql-server")
	names, _, _ := unstructured.NestedStringSlice(server.Object, "spec", "dnsNames")
	if !slices.Contains(names, MySQLPrimaryHost(opts.ProjectID, opts.Namespace)) {
		t.Errorf("server certificate lacks the primary host: %v", names)
	}
}

// The cluster presents the issued certificate: the secret is mounted into
// mysqld and the settings point mysqld at it.
func TestMySQLClusterServesTheIssuedCertificate(t *testing.T) {
	cluster, err := BuildMySQLCluster(mysqlOpts(freePlan))
	if err != nil {
		t.Fatal(err)
	}
	volumes, _, _ := unstructured.NestedSlice(cluster.Object, "spec", "podTemplate", "spec", "volumes")
	if len(volumes) != 1 {
		t.Fatalf("volumes %v", volumes)
	}
	secret, _, _ := unstructured.NestedString(volumes[0].(map[string]interface{}), "secret", "secretName")
	if secret != MySQLServerTLSSecretName("proj-abc") {
		t.Errorf("mounted secret %q", secret)
	}
	mounts, _, _ := unstructured.NestedSlice(mysqldContainer(t, cluster), "volumeMounts")
	if len(mounts) != 1 {
		t.Fatalf("mounts %v", mounts)
	}
	mount := mounts[0].(map[string]interface{})
	if mount["mountPath"] != MySQLTLSDir || mount["readOnly"] != true || mount["subPath"] != nil {
		t.Errorf("mount %v (a subPath mount would never see a renewed certificate)", mount)
	}

	settings, err := BuildMySQLConfig(mysqlOpts(freePlan))
	if err != nil {
		t.Fatal(err)
	}
	data, _, _ := unstructured.NestedStringMap(settings.Object, "data")
	if data["ssl_cert"] != MySQLTLSDir+"/tls.crt" || data["ssl_key"] != MySQLTLSDir+"/tls.key" {
		t.Errorf("settings %v", data)
	}
	// MySQL refuses ALTER INSTANCE RELOAD TLS for a certificate it cannot
	// chain to a CA, so a renewed certificate would never be served.
	if data["ssl_ca"] != MySQLTLSDir+"/ca.crt" {
		t.Errorf("ssl_ca %q: the reload sweep cannot pick up a renewed certificate without it", data["ssl_ca"])
	}
	if _, set := data["require_secure_transport"]; set {
		t.Error("MOCO's controller talks to mysqld without TLS; the platform users are REQUIRE SSL instead")
	}
}

// A plan change rebuilds the settings and must keep the certificate.
func TestWithMySQLTierKeepsTheCertificate(t *testing.T) {
	cluster, _ := BuildMySQLCluster(mysqlOpts(freePlan))
	moved, err := WithMySQLTier(cluster, standardPlan, "50Gi")
	if err != nil {
		t.Fatal(err)
	}
	if mounts, _, _ := unstructured.NestedSlice(mysqldContainer(t, moved), "volumeMounts"); len(mounts) != 1 {
		t.Errorf("mounts after a plan change %v", mounts)
	}
}

func TestMySQLServerCAFromSecret(t *testing.T) {
	ca := MockClusterCA()["ca.crt"]
	got, err := MySQLServerCA(map[string][]byte{"tls.crt": ca, "ca.crt": ca, "tls.key": []byte("k")})
	if err != nil || got != string(ca) {
		t.Fatalf("got %q, %v", got, err)
	}
	for name, data := range map[string]map[string][]byte{
		"missing": {}, "garbage": {"tls.crt": []byte("no")},
	} {
		if _, err := MySQLServerCA(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
