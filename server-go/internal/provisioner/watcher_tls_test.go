package provisioner

import (
	"context"
	"errors"
	"net/url"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

const watcherTLSSecret = "org1-proj/proj-cdc-watcher-tls"

func deployTestWatcher(t *testing.T, mock *k8s.MockClient, spec WatcherSpec) map[string]interface{} {
	t.Helper()
	prov := NewPostgreSQLProvisioner(mock, watcherChartDir)
	prov.SetWatcherImage(pinnedWatcher)
	if err := prov.DeployWatcher(context.Background(), spec); err != nil {
		t.Fatalf("DeployWatcher: %v", err)
	}
	return mock.HelmReleases["org1-proj/"+watcherReleaseName]
}

// cdc_watcher logs in with its certificate only (EXC-410): the stream is
// verify-full against the cluster CA and presents the mounted client cert.
func TestTheWatcherStreamsWithItsClientCertificate(t *testing.T) {
	mock := k8s.NewMockClient()
	values := deployTestWatcher(t, mock, watcherSpecForTest())

	postgres := values["postgres"].(map[string]interface{})
	parsed, err := url.Parse(postgres["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	want := map[string]string{
		"sslmode":     "verify-full",
		"sslcert":     watcherTLSMountPath + "/tls.crt",
		"sslkey":      watcherTLSMountPath + "/tls.key",
		"sslrootcert": watcherTLSMountPath + "/ca.crt",
		"replication": "database",
	}
	for key, value := range want {
		if got := query.Get(key); got != value {
			t.Errorf("url %s = %q, want %q", key, got, value)
		}
	}
	if postgres["password"] != "" {
		t.Error("the watcher is still handed a password")
	}
	secret := mock.Secrets[watcherTLSSecret]
	if string(secret["tls.crt"]) != "CERT-PEM" || string(secret["tls.key"]) != "KEY-PEM" || string(secret["ca.crt"]) != "CA-PEM" {
		t.Errorf("certificate secret = %v", secret)
	}
}

func TestTheWatcherMountsItsCertificateSecret(t *testing.T) {
	values := deployTestWatcher(t, k8s.NewMockClient(), watcherSpecForTest())
	volumes := values["extraVolumes"].([]interface{})
	volume := volumes[0].(map[string]interface{})
	source := volume["secret"].(map[string]interface{})
	if source["secretName"] != "proj-cdc-watcher-tls" || source["defaultMode"] != 0o440 {
		t.Errorf("volume = %v", volume)
	}
	mount := values["extraVolumeMounts"].([]interface{})[0].(map[string]interface{})
	if mount["mountPath"] != watcherTLSMountPath || mount["readOnly"] != true || mount["name"] != volume["name"] {
		t.Errorf("mount = %v", mount)
	}
}

// A renewed certificate replaces the secret and changes the pod template, so
// the watcher restarts onto it rather than holding the old one in memory.
func TestARenewedCertificateRestartsTheWatcher(t *testing.T) {
	mock := k8s.NewMockClient()
	first := deployTestWatcher(t, mock, watcherSpecForTest())["podAnnotations"].(map[string]interface{})
	renewed := watcherSpecForTest()
	renewed.ClientCert = tenantcert.Material{Cert: "CERT-2", Key: "KEY-2", RootCert: "CA-PEM"}
	second := deployTestWatcher(t, mock, renewed)["podAnnotations"].(map[string]interface{})
	if first[watcherCertAnnotation] == second[watcherCertAnnotation] || second[watcherCertAnnotation] == "" {
		t.Errorf("pod annotation did not change: %v -> %v", first, second)
	}
	if string(mock.Secrets[watcherTLSSecret]["tls.crt"]) != "CERT-2" {
		t.Error("the certificate secret was not replaced")
	}
}

func TestTheWatcherIsNotDeployedWithoutACertificate(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, watcherChartDir)
	prov.SetWatcherImage(pinnedWatcher)
	spec := watcherSpecForTest()
	spec.ClientCert = tenantcert.Material{}
	if err := prov.DeployWatcher(context.Background(), spec); err == nil {
		t.Fatal("a watcher without a certificate was deployed")
	}
	if len(mock.HelmReleases) != 0 {
		t.Error("the chart was installed anyway")
	}
}

func TestAnExistingCertificateSecretIsReplaced(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.Secrets[watcherTLSSecret] = map[string][]byte{"tls.crt": []byte("OLD")}
	mock.CreateSecretError = apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, "proj-cdc-watcher-tls")
	deployTestWatcher(t, mock, watcherSpecForTest())
	if string(mock.Secrets[watcherTLSSecret]["tls.crt"]) != "CERT-PEM" {
		t.Error("the existing secret was not updated")
	}
}

func TestAFailedCertificateSecretStopsTheDeploy(t *testing.T) {
	mock := k8s.NewMockClient()
	mock.CreateSecretError = errors.New("forbidden")
	prov := NewPostgreSQLProvisioner(mock, watcherChartDir)
	prov.SetWatcherImage(pinnedWatcher)
	if err := prov.DeployWatcher(context.Background(), watcherSpecForTest()); err == nil || len(mock.HelmReleases) != 0 {
		t.Fatalf("err = %v, releases = %d", err, len(mock.HelmReleases))
	}
}
