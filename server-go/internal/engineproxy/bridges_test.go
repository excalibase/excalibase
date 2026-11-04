package engineproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EXC-683: the host firewall matches the platform's bridges by their real
// interface, which the engine alone knows: a network made with a bridge name
// option has that name, any other Docker's br-<first 12 of its id>, whatever
// compose now says (an upgrade keeps the network it already has).

func bridgeEngine(t *testing.T, networks map[string]any) *Handler {
	t.Helper()
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(stripVersion(r.URL.Path), "/networks/")
		network, ok := networks[name]
		if r.Method != http.MethodGet || !ok {
			http.Error(w, `{"message":"network not found"}`, http.StatusNotFound)
			return
		}
		if network == nil {
			http.Error(w, `{"message":"engine down"}`, http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(network)
	}))
	t.Cleanup(engine.Close)
	target, _ := url.Parse(engine.URL)
	return NewHandler(testPolicy(), target, http.DefaultTransport)
}

func networkJSON(id string, options map[string]string, subnets ...string) map[string]any {
	config := []map[string]string{}
	for _, subnet := range subnets {
		config = append(config, map[string]string{"Subnet": subnet})
	}
	return map[string]any{"Id": id, "Driver": "bridge", "Options": options, "IPAM": map[string]any{"Config": config}}
}

func TestResolveBridgesNamesEachNetworksHostInterface(t *testing.T) {
	handler := bridgeEngine(t, map[string]any{
		// Made by v1.6: no name option, so br-<id[:12]>.
		"excalibase-tenants": networkJSON("3f1c9a2b7d4e5f60718293a4b5c6d7e8f9", nil, "172.19.0.0/16", "fd00:19::/64"),
		// Made with a name option: that name.
		"excalibase-platform": networkJSON("aa11bb22cc33dd44", map[string]string{"com.docker.network.bridge.name": "exb-platform"}, "172.20.0.0/16"),
	})
	lines, err := handler.ResolveBridges(context.Background(), []string{"excalibase-tenants", "excalibase-platform", "excalibase-logs"})
	if err != nil {
		t.Fatalf("ResolveBridges: %v", err)
	}
	want := []string{
		"excalibase-tenants br-3f1c9a2b7d4e 172.19.0.0/16,fd00:19::/64",
		"excalibase-platform exb-platform 172.20.0.0/16",
		// Its profile is off: compose made no such network.
		"excalibase-logs absent",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// A network the engine cannot answer for, or one that is no bridge, is never
// guessed at: it gets no line, so the firewall sees it unresolved and refuses
// to report healthy, while the networks that did resolve keep their lines.
func TestResolveBridgesRefusesWhatItCannotName(t *testing.T) {
	cases := map[string]any{
		"engine error":   nil,
		"not a bridge":   map[string]any{"Id": "3f1c9a2b7d4e5f60", "Driver": "macvlan"},
		"short id":       networkJSON("3f1c", nil, "172.19.0.0/16"),
		"bad name":       networkJSON("3f1c9a2b7d4e5f60", map[string]string{"com.docker.network.bridge.name": "br 1; reboot"}),
		"name too long":  networkJSON("3f1c9a2b7d4e5f60", map[string]string{"com.docker.network.bridge.name": "exb-sixteen-char"}),
		"subnet garbage": networkJSON("3f1c9a2b7d4e5f60", nil, "172.19.0.0/16 -j ACCEPT"),
	}
	for name, network := range cases {
		handler := bridgeEngine(t, map[string]any{"excalibase-tenants": network})
		lines, err := handler.ResolveBridges(context.Background(), []string{"excalibase-tenants"})
		if err == nil || len(lines) != 0 {
			t.Errorf("%s: resolved %v (err %v)", name, lines, err)
		}
	}
}

func TestWriteBridgesReplacesTheFileWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridges")
	if err := WriteBridges(path, []string{"excalibase-tenants br-3f1c9a2b7d4e 172.19.0.0/16"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteBridges(path, []string{"excalibase-tenants br-0000aaaabbbb 172.21.0.0/16"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "excalibase-tenants br-0000aaaabbbb 172.21.0.0/16\n" {
		t.Fatalf("file %q err %v", raw, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v: the firewall, another user, must read it", info.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("left behind %v", entries)
	}
}
