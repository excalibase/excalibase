//go:build integration

package provisioner

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/engineproxy"
)

// EXC-682 where the bug escaped: provisioning reaches the engine through the
// engine proxy, as on a single host. A project v1.6 created has a data volume
// labelled managed and nothing else; after the update its minor upgrade
// recreates the container over that volume, keeping its rows.
//
// Run: go test -tags=integration ./internal/provisioner/ -run TestDockerMySQLUpgradeThroughTheProxy -v
func TestDockerMySQLUpgradeThroughTheProxyOpensAV16Volume(t *testing.T) {
	direct := requireDocker(t)
	defer direct.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	projectID := fmt.Sprintf("it-myv16-%d", time.Now().UnixNano()%1_000_000)
	legacy := map[string]string{excalibaseLabel: "true"}
	if _, err := direct.c.VolumeCreate(ctx, volume.CreateOptions{Name: MySQLVolumeName(projectID), Driver: "local", Labels: legacy}); err != nil {
		t.Fatalf("the v1.6 volume: %v", err)
	}
	proxied := throughEngineProxy(t)
	p := NewDockerMySQLProvisioner(proxied)
	t.Cleanup(func() { _ = NewDockerMySQLProvisioner(direct).Deprovision(context.Background(), "", projectID) })

	plan := config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}
	req := domain.ProvisioningRequest{ProjectName: projectID, DBType: domain.MySQL, DatabaseName: "shop", MySQLVersion: "8.4.9"}
	result, err := p.Provision(ctx, req, plan, func(domain.ProvisioningStage) {})
	if err != nil {
		t.Fatalf("Provision over the v1.6 volume: %v", err)
	}
	if labels, err := direct.VolumeLabels(ctx, MySQLVolumeName(projectID)); err != nil || labels[projectLabel] != "" {
		t.Fatalf("the engine labelled an existing volume (%v, %v): this test no longer models v1.6", labels, err)
	}
	if _, err := p.run(ctx, result.Namespace, "CREATE TABLE shop.notes (id INT PRIMARY KEY); INSERT INTO shop.notes VALUES (1), (2);"); err != nil {
		t.Fatalf("rows: %v", err)
	}

	newest, _ := config.MySQLImage("8.4.10")
	if err := p.ReplaceImage(ctx, projectID, newest, plan); err != nil {
		t.Fatalf("ReplaceImage over the v1.6 volume: %v", err)
	}
	out, err := p.run(ctx, result.Namespace, "SELECT VERSION(), COUNT(*) FROM shop.notes;")
	if err != nil || strings.Join(strings.Fields(out), " ") != "8.4.10 2" {
		t.Fatalf("after the upgrade: %q %v", out, err)
	}
}

// throughEngineProxy is a client that reaches the engine through the engine
// proxy under the single host's volume and container names.
func throughEngineProxy(t *testing.T) *RealDockerClient {
	t.Helper()
	socket := strings.TrimPrefix(client.DefaultDockerHost, "unix://")
	if host := os.Getenv("DOCKER_HOST"); strings.HasPrefix(host, "unix://") {
		socket = strings.TrimPrefix(host, "unix://")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	policy := engineproxy.Policy{ManagedLabel: engineproxy.ManagedLabel, PortBindIPs: []string{"127.0.0.1"},
		VolumePrefix: MySQLVolumePrefix, ContainerPrefix: "excalibase-"}
	proxy := httptest.NewServer(engineproxy.NewHandler(policy, &url.URL{Scheme: "http", Host: "engine"}, transport))
	t.Cleanup(proxy.Close)
	proxied, err := NewRealDockerClient(DockerClientOptions{Host: "tcp://" + strings.TrimPrefix(proxy.URL, "http://")})
	if err != nil {
		t.Fatalf("client through the proxy: %v", err)
	}
	t.Cleanup(func() { _ = proxied.Close() })
	return proxied
}
