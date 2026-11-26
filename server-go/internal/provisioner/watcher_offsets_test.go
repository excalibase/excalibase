package provisioner

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"sigs.k8s.io/yaml"
)

const (
	singleHostCompose = "../../../deploy/single-host/compose.yaml"
	offsetsBucket     = "WATCHER_MYSQL_OFFSETS"
)

func renderWatcherChart(t *testing.T, overrides map[string]interface{}) map[string]string {
	t.Helper()
	chart, err := loader.Load(watcherChartDir)
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	options := chartutil.ReleaseOptions{Name: watcherReleaseName, Namespace: "org1-proj", IsInstall: true}
	values, err := chartutil.ToRenderValues(chart, overrides, options, chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatalf("render values: %v", err)
	}
	manifests, err := engine.Render(chart, values)
	if err != nil {
		t.Fatalf("render chart: %v", err)
	}
	return manifests
}

func streamInitJob(t *testing.T, overrides map[string]interface{}) string {
	t.Helper()
	for name, manifest := range renderWatcherChart(t, overrides) {
		if strings.HasSuffix(name, "stream-init-job.yaml") {
			return manifest
		}
	}
	t.Fatal("chart renders no stream-init job")
	return ""
}

// The single-host install runs the one job that makes the bus ready; a MySQL
// watcher must find its offsets bucket there, not create it as a tenant.
func TestSingleHostBusInitCreatesTheOffsetsBucket(t *testing.T) {
	raw, err := os.ReadFile(singleHostCompose)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	var compose struct {
		Services map[string]struct {
			Command []string `json:"command"`
		} `json:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	script := strings.Join(compose.Services["nats-stream-init"].Command, "\n")
	if !strings.Contains(script, "nats kv add "+offsetsBucket) {
		t.Errorf("nats-stream-init does not create %s:\n%s", offsetsBucket, script)
	}
	if !strings.Contains(script, "nats kv info "+offsetsBucket) {
		t.Errorf("nats-stream-init does not check for %s first, so a second start fails", offsetsBucket)
	}
}

func TestChartStreamInitCreatesTheOffsetsBucketForMySQL(t *testing.T) {
	job := streamInitJob(t, map[string]interface{}{
		"mysql": map[string]interface{}{"enabled": true, "caCert": "pem"},
		"nats":  map[string]interface{}{"enabled": true, "streamInit": map[string]interface{}{"enabled": true}},
	})
	if !strings.Contains(job, "nats --server=nats://nats:4222 kv add "+offsetsBucket) {
		t.Errorf("job does not create %s:\n%s", offsetsBucket, job)
	}
	if !strings.Contains(job, "--replicas=3") {
		t.Errorf("bucket is not replicated like the chart's offsetReplicas (3):\n%s", job)
	}
}

func TestChartStreamInitLeavesPostgresOnlyInstallsWithoutABucket(t *testing.T) {
	job := streamInitJob(t, map[string]interface{}{
		"nats": map[string]interface{}{"enabled": true, "streamInit": map[string]interface{}{"enabled": true}},
	})
	if strings.Contains(job, offsetsBucket) {
		t.Errorf("a Postgres watcher gets a MySQL offsets bucket:\n%s", job)
	}
}

// The chart is copied from the watcher repo, so its version has to be the
// watcher the platform runs; the compose pin names that version.
func TestWatcherChartIsPinnedToTheWatcherWeDeploy(t *testing.T) {
	chartRaw, err := os.ReadFile(watcherChartDir + "/Chart.yaml")
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	var meta struct {
		AppVersion string `json:"appVersion"`
	}
	if err := yaml.Unmarshal(chartRaw, &meta); err != nil {
		t.Fatalf("parse Chart.yaml: %v", err)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(meta.AppVersion) {
		t.Fatalf("chart appVersion %q is not a concrete version", meta.AppVersion)
	}
	compose, err := os.ReadFile(singleHostCompose)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	want := "# watcher " + meta.AppVersion + "\n"
	if !strings.Contains(string(compose), want) {
		t.Errorf("compose does not say its watcher pin is version %s (%q)", meta.AppVersion, want)
	}
}
