//go:build integration

package engineproxy

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/network"
)

// EXC-683 on the real engine: a network made without a bridge name (as v1.6
// made every platform network) and one made with it each resolve to the
// interface the host really has.
//
// Run: go test -tags=integration ./internal/engineproxy/ -run TestRealEngineResolvesBridges -v
func TestRealEngineResolvesBridges(t *testing.T) {
	direct, socketClient, _ := realEngine(t)
	suffix := strconv.FormatInt(time.Now().UnixNano()%1_000_000_000, 36)
	plain := "excalibase-it683-plain-" + suffix
	named := "excalibase-it683-named-" + suffix
	bridge := "exbit" + suffix
	if len(bridge) > 15 {
		bridge = bridge[:15]
	}
	for name, options := range map[string]map[string]string{plain: nil, named: {bridgeNameOption: bridge}} {
		if _, err := direct.NetworkCreate(context.Background(), name, network.CreateOptions{Driver: "bridge", Options: options}); err != nil {
			t.Fatalf("network %s: %v", name, err)
		}
		t.Cleanup(func() { _ = direct.NetworkRemove(context.Background(), name) })
	}
	inspected, err := direct.NetworkInspect(context.Background(), plain, network.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(testPolicy(), &url.URL{Scheme: "http", Host: "engine"}, socketClient.Transport.(*http.Transport))
	lines, err := handler.ResolveBridges(context.Background(), []string{plain, named, "excalibase-it683-none-" + suffix})
	if err != nil {
		t.Fatalf("ResolveBridges: %v", err)
	}
	wantIfaces := []string{"br-" + inspected.ID[:12], bridge}
	for i, iface := range wantIfaces {
		fields := strings.Fields(lines[i])
		if len(fields) != 3 || fields[1] != iface {
			t.Fatalf("line %q, want bridge %s", lines[i], iface)
		}
		if _, err := os.Stat("/sys/class/net/" + iface); err != nil {
			t.Errorf("bridge %s is not on this host: %v", iface, err)
		}
	}
	if !strings.HasSuffix(lines[2], " absent") {
		t.Fatalf("a missing network: %q", lines[2])
	}
}
