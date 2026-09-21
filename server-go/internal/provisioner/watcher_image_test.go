package provisioner

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const watcherChartValues = "../../charts/excalibase-watcher-go/values.yaml"

// EXC-432: the chart defaulted to ghcr.io/excalibase/watcher, which does not
// exist — the registry answers 404 — and nobody noticed because provisioning
// always passes its own repository. Anyone installing the chart directly got
// a pod that could never pull. The default and what we install with have to
// be the same image.
func TestWatcherChartDefaultsToTheImageWeInstall(t *testing.T) {
	raw, err := os.ReadFile(watcherChartValues)
	if err != nil {
		t.Fatalf("read %s: %v", watcherChartValues, err)
	}
	var values struct {
		Image struct {
			Repository string `json:"repository"`
		} `json:"image"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatalf("parse %s: %v", watcherChartValues, err)
	}

	if values.Image.Repository != watcherImageRepository {
		t.Errorf("chart default %q, provisioning installs %q",
			values.Image.Repository, watcherImageRepository)
	}
}

// Docker Hub is where the platform pulls from. An image on a registry a
// tenant's node has no credential for is the same as no image at all.
func TestWatcherImageIsNotOnAPrivateRegistry(t *testing.T) {
	if strings.Contains(watcherImageRepository, "ghcr.io") {
		t.Errorf("watcher image %q is not on Docker Hub", watcherImageRepository)
	}
}
