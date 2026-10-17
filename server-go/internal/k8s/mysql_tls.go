package k8s

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// A MySQL project's server certificate (EXC-589). MOCO leaves mysqld on its
// self-signed certificate, which no client can verify, so each project gets
// a cert-manager CA of its own, like CNPG's per-cluster CA for Postgres, and
// mysqld presents a certificate that CA issued.

// IssuerGVR is cert-manager's namespaced Issuer.
var IssuerGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}

const (
	// MySQLTLSDir is where mysqld reads its certificate. A whole-secret mount,
	// never a subPath, so a renewed certificate appears in place.
	MySQLTLSDir      = "/etc/excalibase/mysql-tls"
	mysqlTLSVolume   = "excalibase-mysql-tls"
	mysqlTLSFileMode = int64(0o440)
	// The CA outlives any project; it keeps its key when renewed, so a
	// certificate it issued before still verifies against the renewed one.
	mysqlCADuration = "87600h"
	// The server certificate lasts 90 days and is renewed 30 days early,
	// leaving the reload sweep weeks to pick it up.
	mysqlServerDuration    = "2160h"
	mysqlServerRenewBefore = "720h"
)

// MySQLTLSObject is one cert-manager object, in the order they are applied.
type MySQLTLSObject struct {
	GVR    schema.GroupVersionResource
	Object *unstructured.Unstructured
}

// MySQLCASecretName holds the project's CA (tls.crt is the CA certificate).
func MySQLCASecretName(projectID string) string { return MySQLClusterName(projectID) + "-ca" }

// MySQLServerTLSSecretName holds the certificate and key mysqld presents.
func MySQLServerTLSSecretName(projectID string) string { return MySQLClusterName(projectID) + "-tls" }

func mysqlSelfSignedIssuer(projectID string) string {
	return MySQLClusterName(projectID) + "-selfsigned"
}
func mysqlCAIssuer(projectID string) string { return MySQLClusterName(projectID) + "-ca-issuer" }
func mysqlServerCertificate(projectID string) string {
	return MySQLClusterName(projectID) + "-server"
}

// MySQLServerNames are the names a client may dial: the primary and replica
// Services in every form a pod resolves, each instance through the headless
// Service, and the public endpoint names.
func MySQLServerNames(projectID, namespace string, public []string) []string {
	cluster := MySQLClusterName(projectID)
	names := make([]string, 0, 12+len(public))
	for _, service := range []string{"moco-" + cluster + "-primary", "moco-" + cluster + "-replica"} {
		names = append(names, service, service+"."+namespace, service+"."+namespace+".svc", service+"."+namespace+".svc.cluster.local")
	}
	headless := "moco-" + cluster + "." + namespace
	names = append(names, "*."+headless, "*."+headless+".svc", "*."+headless+".svc.cluster.local")
	return append(names, public...)
}

// BuildMySQLTLS renders the project's CA and server certificate.
func BuildMySQLTLS(opts MySQLClusterOpts) []MySQLTLSObject {
	id, namespace := opts.ProjectID, opts.Namespace
	selfSigned := mysqlTLSResource("Issuer", mysqlSelfSignedIssuer(id), opts, map[string]interface{}{"selfSigned": map[string]interface{}{}})
	ca := mysqlTLSResource("Certificate", MySQLCASecretName(id), opts, map[string]interface{}{
		"isCA":       true,
		"commonName": id + " MySQL CA",
		"secretName": MySQLCASecretName(id),
		"duration":   mysqlCADuration,
		"privateKey": map[string]interface{}{"algorithm": "ECDSA", "size": int64(256), "rotationPolicy": "Never"},
		"issuerRef":  mysqlIssuerRef(mysqlSelfSignedIssuer(id)),
	})
	caIssuer := mysqlTLSResource("Issuer", mysqlCAIssuer(id), opts, map[string]interface{}{
		"ca": map[string]interface{}{"secretName": MySQLCASecretName(id)},
	})
	// No commonName: cert-manager caps it at 64 bytes, which a Service name in
	// a real namespace exceeds; clients verify against dnsNames.
	server := mysqlTLSResource("Certificate", mysqlServerCertificate(id), opts, map[string]interface{}{
		"dnsNames":    toInterfaces(MySQLServerNames(id, namespace, opts.PublicNames)),
		"secretName":  MySQLServerTLSSecretName(id),
		"duration":    mysqlServerDuration,
		"renewBefore": mysqlServerRenewBefore,
		"usages":      []interface{}{"server auth", "digital signature", "key encipherment"},
		"privateKey":  map[string]interface{}{"algorithm": "ECDSA", "size": int64(256), "rotationPolicy": "Always"},
		"issuerRef":   mysqlIssuerRef(mysqlCAIssuer(id)),
	})
	return []MySQLTLSObject{
		{GVR: IssuerGVR, Object: selfSigned},
		{GVR: CertificateGVR, Object: ca},
		{GVR: IssuerGVR, Object: caIssuer},
		{GVR: CertificateGVR, Object: server},
	}
}

func mysqlTLSResource(kind, name string, opts MySQLClusterOpts, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": CertificateGVR.GroupVersion().String(),
		"kind":       kind,
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": opts.Namespace,
			"labels":    map[string]interface{}{"excalibase.io/project": opts.ProjectID},
		},
		"spec": spec,
	}}
}

func mysqlIssuerRef(name string) map[string]interface{} {
	return map[string]interface{}{"name": name, "kind": "Issuer", "group": CertificateGVR.Group}
}

func toInterfaces(values []string) []interface{} {
	out := make([]interface{}, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// mountMySQLTLS mounts the server certificate secret into mysqld.
func mountMySQLTLS(podSpec, mysqld map[string]interface{}, projectID string) {
	podSpec["volumes"] = []interface{}{map[string]interface{}{
		"name": mysqlTLSVolume,
		"secret": map[string]interface{}{
			"secretName": MySQLServerTLSSecretName(projectID), "defaultMode": mysqlTLSFileMode,
		},
	}}
	mysqld["volumeMounts"] = []interface{}{map[string]interface{}{
		"name": mysqlTLSVolume, "mountPath": MySQLTLSDir, "readOnly": true,
	}}
}

// mysqlTLSSettings point mysqld at the mounted certificate. ssl_ca is the
// issuing CA cert-manager writes beside it: without it MySQL refuses
// ALTER INSTANCE RELOAD TLS, so a renewed certificate is never served.
func mysqlTLSSettings() map[string]string {
	return map[string]string{
		"ssl_ca": MySQLTLSDir + "/ca.crt", "ssl_cert": MySQLTLSDir + "/tls.crt", "ssl_key": MySQLTLSDir + "/tls.key",
	}
}

// MySQLServerCA reads the CA certificate out of the CA secret.
func MySQLServerCA(secret map[string][]byte) (string, error) {
	caPEM := secret["tls.crt"]
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return "", errors.New("the MySQL CA secret holds no certificate yet")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return "", fmt.Errorf("the MySQL CA secret holds no CA certificate")
	}
	return string(caPEM), nil
}
