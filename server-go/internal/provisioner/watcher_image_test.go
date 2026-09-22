package provisioner

import (
	"context"
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const watcherChartValues = "../../charts/excalibase-watcher-go/values.yaml"

// The chart default only ever runs when something other than provisioning
// installs it, so nothing catches it drifting from the override (EXC-432).
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

// Blank chart path = watcher disabled; provisioning must still succeed.
func TestDeployWatcherWithoutAChartInstallsNothing(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, "")

	if err := prov.DeployWatcher(context.Background(), WatcherSpec{Namespace: "org1-proj"}); err != nil {
		t.Fatalf("DeployWatcher: %v", err)
	}
	if len(mock.HelmReleases) != 0 {
		t.Errorf("installed %v with no chart path", mock.HelmReleases)
	}
}

// Tenant nodes hold no registry credential, so only Docker Hub pulls.
func TestWatcherImageIsNotOnAPrivateRegistry(t *testing.T) {
	if strings.Contains(watcherImageRepository, "ghcr.io") {
		t.Errorf("watcher image %q is not on Docker Hub", watcherImageRepository)
	}
}
