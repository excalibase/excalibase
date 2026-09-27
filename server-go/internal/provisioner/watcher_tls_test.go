package provisioner

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// The watcher's replication stream carries every row; it must not fall back
// to plaintext against a database that requires TLS.
func TestTheWatcherStreamsOverTLS(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, watcherChartDir)
	prov.SetWatcherImage(pinnedWatcher)
	if err := prov.DeployWatcher(context.Background(), watcherSpecForTest()); err != nil {
		t.Fatalf("DeployWatcher: %v", err)
	}
	postgres := mock.HelmReleases["org1-proj/"+watcherReleaseName]["postgres"].(map[string]interface{})
	url := postgres["url"].(string)
	if !strings.Contains(url, "sslmode=require") || !strings.Contains(url, "replication=database") {
		t.Fatalf("watcher url = %q", url)
	}
}
