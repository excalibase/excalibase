package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

type nullNetStore struct{}

func (nullNetStore) AppendNetSamples(context.Context, []netmeter.Sample) error { return nil }
func (nullNetStore) PruneNetSamples(context.Context, time.Time) error          { return nil }

type nullCluster struct{}

func (nullCluster) NodeAddresses(context.Context) ([]string, error)    { return nil, nil }
func (nullCluster) AppPods(context.Context) ([]netmeter.AppPod, error) { return nil, nil }

func netMeterConfig(t *testing.T, mode, flag string) config.AppConfig {
	t.Helper()
	t.Setenv("CORS_ORIGINS", "https://app.test")
	t.Setenv("PROVISIONER_MODE", mode)
	t.Setenv("FEATURE_NETMETER", flag)
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	return config.Load()
}

func TestNetMeterSampler_OffWhileTheFlagIsOff(t *testing.T) {
	t.Setenv("NETMETER_KUBELET_CA_FILE", writeTestCA(t))
	cfg := netMeterConfig(t, "", "")
	if sampler := netMeterSampler(cfg, nullNetStore{}, nullCluster{}); sampler != nil {
		t.Fatal("flag off: nothing is built, nothing is read")
	}
}

func TestNetMeterSampler_KubernetesNeedsAUsableKubeletCA(t *testing.T) {
	t.Setenv("NETMETER_KUBELET_CA_FILE", filepath.Join(t.TempDir(), "missing.crt"))
	cfg := netMeterConfig(t, "", "true")
	if sampler := netMeterSampler(cfg, nullNetStore{}, nullCluster{}); sampler != nil {
		t.Fatal("no CA file: the meter stays off, it never falls back to an unverified client")
	}
}

func TestNetMeterSampler_KubernetesWithCAAndCluster(t *testing.T) {
	t.Setenv("NETMETER_KUBELET_CA_FILE", writeTestCA(t))
	cfg := netMeterConfig(t, "", "true")
	if sampler := netMeterSampler(cfg, nullNetStore{}, nullCluster{}); sampler == nil {
		t.Fatal("flag on with a CA and a cluster client: the meter runs")
	}
	if sampler := netMeterSampler(cfg, nullNetStore{}, nil); sampler != nil {
		t.Fatal("no cluster client: off")
	}
	if sampler := netMeterSampler(cfg, struct{}{}, nullCluster{}); sampler != nil {
		t.Fatal("a store without the metering tables: off")
	}
}

func TestNetMeterSampler_SingleHostNeedsNoKubeletAndNoHostMetricsFlag(t *testing.T) {
	cfg := netMeterConfig(t, "docker", "true")
	if sampler := netMeterSampler(cfg, nullNetStore{}, nil); sampler == nil {
		t.Fatal("single host meters from the engine; FEATURE_HOSTMETRICS is not needed")
	}
}

func TestMetricsLoopRuns_SingleHostWithNetMeterAlone(t *testing.T) {
	cfg := netMeterConfig(t, "docker", "true")
	if !metricsLoopRuns(cfg, nil, nil) {
		t.Fatal("the loop that samples must run for the meter alone on a single host")
	}
	cfg = netMeterConfig(t, "docker", "")
	if metricsLoopRuns(cfg, nil, nil) {
		t.Fatal("and not run when nothing needs it")
	}
}

// writeTestCA writes a throwaway self-signed CA certificate, only parsed.
func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
