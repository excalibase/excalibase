package engineproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	rejoinProject = "p1"
	webService    = "web." + AppServiceDomain
)

// rejoinRig is an engine with one project's network and one app, as the engine reports them.
// Docker reports an app's mode as its network's name; Podman's Docker-compatible API reports
// "bridge" whatever the network is.
func rejoinRig(t *testing.T, mode string) (*restartEngine, *Handler) {
	t.Helper()
	app := egressApp("running")
	app.mode = mode
	app.labels[appNameLabel] = "web"
	app.labels[projectLabel] = rejoinProject
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"app": app})
	engine.networks = map[string]map[string]any{
		egressNet:                 {"Driver": "bridge", "Labels": map[string]string{ManagedLabel: "true", projectLabel: rejoinProject}},
		"excalibase-proj-other":   {"Driver": "bridge", "Labels": map[string]string{ManagedLabel: "true", projectLabel: "p2"}},
		"excalibase-proj-nolabel": {"Driver": "bridge", "Labels": map[string]string{ManagedLabel: "true"}},
		"excalibase-proj-unowned": {"Driver": "bridge", "Labels": map[string]string{projectLabel: rejoinProject}},
	}
	return engine, handler
}

// connect sends the call the engine client makes when it puts an app back on a network.
func connect(t *testing.T, handler *Handler, network string, body map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/networks/"+network+"/connect", strings.NewReader(string(raw)))
	return handler.authorize(req)
}

func member(container string, endpoint map[string]any) map[string]any {
	return map[string]any{"Container": container, "EndpointConfig": endpoint}
}

// dockerEndpoint and podmanEndpoint are the endpoint the same client sends; the Docker SDK
// fills in every field of the settings, Podman's compatibility layer reads them the same.
func dockerEndpoint(aliases ...string) map[string]any {
	return map[string]any{"Aliases": aliases}
}

func podmanEndpoint(aliases ...string) map[string]any {
	return map[string]any{"Aliases": aliases, "DNSNames": nil, "DriverOpts": nil, "EndpointID": "", "Gateway": "",
		"GlobalIPv6Address": "", "GlobalIPv6PrefixLen": 0, "GwPriority": 0, "IPAMConfig": nil, "IPAddress": "",
		"IPPrefixLen": 0, "IPv6Gateway": "", "Links": nil, "MacAddress": "", "NetworkID": ""}
}

func TestAnAppRejoinsItsOwnNetworkWhicheverModeTheEngineReports(t *testing.T) {
	for label, mode := range map[string]string{"Docker names the network": egressNet, "Podman says bridge": "bridge"} {
		for shape, endpoint := range map[string]map[string]any{"plain": dockerEndpoint(webService), "full": podmanEndpoint(webService)} {
			_, handler := rejoinRig(t, mode)
			if err := connect(t, handler, egressNet, member("app", endpoint)); err != nil {
				t.Errorf("%s, %s endpoint: %v", label, shape, err)
			}
		}
	}
}

func TestAnAppIsRefusedEveryOtherRejoin(t *testing.T) {
	cases := map[string]struct {
		mode    string
		network string
		body    map[string]any
	}{
		"Docker, another service name":      {egressNet, egressNet, member("app", dockerEndpoint("db."+AppServiceDomain))},
		"Podman, another service name":      {"bridge", egressNet, member("app", podmanEndpoint("db."+AppServiceDomain))},
		"Podman, an extra alias":            {"bridge", egressNet, member("app", podmanEndpoint(webService, "db."+AppServiceDomain))},
		"Podman, a bare alias":              {"bridge", egressNet, member("app", podmanEndpoint("web"))},
		"Podman, another project's network": {"bridge", "excalibase-proj-other", member("app", podmanEndpoint(webService))},
		"Podman, a network with no project": {"bridge", "excalibase-proj-nolabel", member("app", podmanEndpoint(webService))},
		"Podman, a network not ours":        {"bridge", "excalibase-proj-unowned", member("app", podmanEndpoint(webService))},
		"Podman, a network that is gone":    {"bridge", "excalibase-proj-gone", member("app", podmanEndpoint(webService))},
		"Podman, a fixed address":           {"bridge", egressNet, member("app", map[string]any{"Aliases": []string{webService}, "IPAMConfig": map[string]any{"IPv4Address": "10.0.0.2"}})},
		"Docker, on a network named bridge": {"bridge", "bridge", member("app", dockerEndpoint(webService))},
		"Docker, another network's app":     {"excalibase-proj-other", egressNet, member("app", dockerEndpoint(webService))},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			_, handler := rejoinRig(t, c.mode)
			if err := connect(t, handler, c.network, c.body); err == nil {
				t.Fatal("admitted")
			}
		})
	}
}

func TestAPodmanAppWithoutAProjectIsNotPutOnAProjectsNetwork(t *testing.T) {
	engine, handler := rejoinRig(t, "bridge")
	delete(engine.containers["app"].labels, projectLabel)
	if err := connect(t, handler, egressNet, member("app", podmanEndpoint(webService))); err == nil {
		t.Fatal("admitted")
	}
}
