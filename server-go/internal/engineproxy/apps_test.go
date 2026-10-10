package engineproxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func appsPolicy() Policy {
	policy := testPolicy()
	policy.EdgeContainer = "excalibase-edge"
	return policy
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// networkCreateBody is what the Docker SDK sends for a project network.
func networkCreateBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"Name": "excalibase-proj-p1", "Driver": "bridge", "Scope": "", "IPAM": nil,
		"Internal": true, "Attachable": false, "Ingress": false, "ConfigOnly": false, "ConfigFrom": nil,
		"Options": nil, "Labels": map[string]any{"excalibase.managed": "true"},
	}
	if mutate != nil {
		mutate(body)
	}
	return mustJSON(t, body)
}

func TestCheckNetworkCreateAllowsAProjectNetwork(t *testing.T) {
	policy := appsPolicy()
	for name, body := range map[string][]byte{
		"internal":           networkCreateBody(t, nil),
		"with egress":        networkCreateBody(t, func(b map[string]any) { b["Internal"] = false }),
		"isolated (podman)":  networkCreateBody(t, func(b map[string]any) { b["Options"] = map[string]any{"isolate": "true"} }),
		"default driver":     networkCreateBody(t, func(b map[string]any) { b["Driver"] = "" }),
		"old client dedupe":  networkCreateBody(t, func(b map[string]any) { b["CheckDuplicate"] = true }),
		"empty IPAM by SDKs": networkCreateBody(t, func(b map[string]any) { b["IPAM"] = map[string]any{"Driver": "", "Config": nil} }),
	} {
		if err := policy.CheckNetworkCreate(body); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
}

func TestCheckNetworkCreateRefuses(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"a platform network": {func(b map[string]any) { b["Name"] = "excalibase-platform" }, "name"},
		"the bare prefix":    {func(b map[string]any) { b["Name"] = "excalibase-proj-" }, "name"},
		"no managed label":   {func(b map[string]any) { b["Labels"] = map[string]any{} }, "label"},
		"another driver":     {func(b map[string]any) { b["Driver"] = "macvlan" }, "Driver"},
		"a fixed subnet (IPAM)": {func(b map[string]any) {
			b["IPAM"] = map[string]any{"Config": []any{map[string]any{"Subnet": "10.203.250.0/28"}}}
		}, "IPAM"},
		"driver options": {func(b map[string]any) { b["Options"] = map[string]any{"com.docker.network.bridge.name": "eth0"} }, "Options"},
		"isolate off":    {func(b map[string]any) { b["Options"] = map[string]any{"isolate": "false"} }, "Options"},
		"swarm scope":    {func(b map[string]any) { b["Scope"] = "swarm" }, "Scope"},
		"ingress":        {func(b map[string]any) { b["Ingress"] = true }, "Ingress"},
		"config from":    {func(b map[string]any) { b["ConfigFrom"] = map[string]any{"Network": "excalibase-platform"} }, "ConfigFrom"},
		"ipv6":           {func(b map[string]any) { b["EnableIPv6"] = true }, "EnableIPv6"},
		"unknown field":  {func(b map[string]any) { b["Future"] = "x" }, "Future"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := appsPolicy().CheckNetworkCreate(networkCreateBody(t, tc.mutate))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

func TestCheckNetworkConnectAllowsOnlyAnEmptyEndpoint(t *testing.T) {
	policy := appsPolicy()
	if err := policy.CheckNetworkConnect(mustJSON(t, map[string]any{"Container": "excalibase-edge"})); err != nil {
		t.Fatalf("plain connect refused: %v", err)
	}
	for name, body := range map[string]map[string]any{
		"an alias":      {"Container": "excalibase-edge", "EndpointConfig": map[string]any{"Aliases": []any{"provisioning"}}},
		"a fixed IP":    {"Container": "excalibase-edge", "EndpointConfig": map[string]any{"IPAMConfig": map[string]any{"IPv4Address": "10.0.0.2"}}},
		"no container":  {"Container": ""},
		"unknown field": {"Container": "x", "Future": true},
	} {
		if err := policy.CheckNetworkConnect(mustJSON(t, body)); err == nil {
			t.Errorf("%s: connect allowed", name)
		}
	}
}

func TestCheckVolumeCreate(t *testing.T) {
	policy := appsPolicy()
	good := map[string]any{"Name": "excalibase-proj-disk-1", "Driver": "local", "Labels": map[string]any{"excalibase.managed": "true"}}
	if err := policy.CheckVolumeCreate(mustJSON(t, good)); err != nil {
		t.Fatalf("app disk refused: %v", err)
	}
	cases := map[string]map[string]any{
		"platform volume":   {"Name": "excalibase-platform-postgres", "Labels": map[string]any{"excalibase.managed": "true"}},
		"anonymous":         {"Labels": map[string]any{"excalibase.managed": "true"}},
		"no managed label":  {"Name": "excalibase-proj-d"},
		"a driver":          {"Name": "excalibase-proj-d", "Driver": "nfs", "Labels": map[string]any{"excalibase.managed": "true"}},
		"bind via options":  {"Name": "excalibase-proj-d", "DriverOpts": map[string]any{"type": "none", "o": "bind", "device": "/"}, "Labels": map[string]any{"excalibase.managed": "true"}},
		"a cluster volume":  {"Name": "excalibase-proj-d", "ClusterVolumeSpec": map[string]any{"Group": "x"}, "Labels": map[string]any{"excalibase.managed": "true"}},
		"an unknown field":  {"Name": "excalibase-proj-d", "Future": 1, "Labels": map[string]any{"excalibase.managed": "true"}},
		"bare prefix names": {"Name": "excalibase-proj-", "Labels": map[string]any{"excalibase.managed": "true"}},
	}
	for name, body := range cases {
		if err := policy.CheckVolumeCreate(mustJSON(t, body)); err == nil {
			t.Errorf("%s: volume create allowed", name)
		}
	}
}

func TestCheckManagedListFilter(t *testing.T) {
	ok := url.Values{"all": {"1"}, "filters": {`{"label":["excalibase.managed=true","excalibase.app=a1"]}`}}
	if err := checkManagedFilter(ok, "excalibase.managed"); err != nil {
		t.Fatalf("filtered list refused: %v", err)
	}
	for name, query := range map[string]url.Values{
		"no filter":          {"all": {"1"}},
		"other label only":   {"filters": {`{"label":["excalibase.app=a1"]}`}},
		"label set to false": {"filters": {`{"label":["excalibase.managed=false"]}`}},
		"label without =":    {"filters": {`{"label":["excalibase.managed"]}`}},
		"two filter params":  {"filters": {`{"label":["excalibase.managed=true"]}`, `{}`}},
		"not json":           {"filters": {`label=excalibase.managed=true`}},
	} {
		if err := checkManagedFilter(query, "excalibase.managed"); err == nil {
			t.Errorf("%s: list allowed", name)
		}
	}
}

func TestCheckCreateAllowsAnAppContainer(t *testing.T) {
	body := createBody(t, func(b map[string]any) {
		host := hostConfig(b)
		host["PortBindings"] = nil
		host["CapDrop"] = []any{"ALL"}
		host["CapAdd"] = []any{"NET_BIND_SERVICE"}
		host["SecurityOpt"] = []any{"no-new-privileges"}
		host["PidsLimit"] = 1024
		host["Runtime"] = "runsc"
		host["NetworkMode"] = "excalibase-proj-p1"
		host["Mounts"] = []any{map[string]any{"Type": "volume", "Source": "excalibase-proj-disk-1", "Target": "/data"}}
		b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-proj-p1": map[string]any{}}}
	})
	if err := appsPolicy().CheckCreate("excalibase-app-1", body); err != nil {
		t.Fatalf("app container refused: %v", err)
	}
	tool := createBody(t, func(b map[string]any) {
		hostConfig(b)["PortBindings"] = nil
		hostConfig(b)["NetworkMode"] = "none"
		b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{}}
	})
	if err := appsPolicy().CheckCreate("excalibase-disk-1", tool); err != nil {
		t.Fatalf("tool container without a network refused: %v", err)
	}
	other := createBody(t, func(b map[string]any) { hostConfig(b)["CapAdd"] = []any{"NET_BIND_SERVICE", "NET_ADMIN"} })
	if err := appsPolicy().CheckCreate("excalibase-app-2", other); err == nil {
		t.Fatal("NET_ADMIN beside NET_BIND_SERVICE allowed")
	}
}

func appsProxyEngine(t *testing.T) (*fakeEngine, string) {
	t.Helper()
	engine, proxy := newProxyWith(t, appsPolicy())
	engine.containers["excalibase-edge"] = map[string]string{"com.docker.compose.service": "edge"}
	return engine, proxy.URL
}

func TestHandlerForwardsTheAppCalls(t *testing.T) {
	engine, proxy := appsProxyEngine(t)
	list := "/v1.47/containers/json?all=1&filters=" + url.QueryEscape(`{"label":["excalibase.managed=true"]}`)
	volumes := "/v1.47/volumes?filters=" + url.QueryEscape(`{"label":["excalibase.managed=true"]}`)
	allowed := []struct{ method, path, body string }{
		{http.MethodGet, "/v1.47/info", ""},
		{http.MethodGet, list, ""},
		{http.MethodGet, "/v1.47/containers/excalibase-p1-postgres/logs?stdout=1&stderr=1&timestamps=1&tail=100", ""},
		{http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/wait", ""},
		{http.MethodPost, "/v1.47/networks/create", string(networkCreateBody(t, nil))},
		{http.MethodGet, "/v1.47/networks/excalibase-proj-p1", ""},
		{http.MethodDelete, "/v1.47/networks/excalibase-proj-p1", ""},
		{http.MethodPost, "/v1.47/networks/excalibase-proj-p1/connect", `{"Container":"excalibase-edge"}`},
		{http.MethodPost, "/v1.47/networks/excalibase-proj-p1/connect", `{"Container":"excalibase-p1-postgres"}`},
		{http.MethodPost, "/v1.47/networks/excalibase-proj-p1/disconnect", `{"Container":"excalibase-edge","Force":true}`},
		{http.MethodPost, "/v1.47/volumes/create", `{"Name":"excalibase-proj-disk","Labels":{"excalibase.managed":"true"}}`},
		{http.MethodGet, "/v1.47/volumes/excalibase-proj-disk", ""},
		{http.MethodDelete, "/v1.47/volumes/excalibase-proj-disk", ""},
		{http.MethodGet, volumes, ""},
	}
	for _, tc := range allowed {
		if resp := call(t, proxy, tc.method, tc.path, tc.body); resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s = %d, want forwarded", tc.method, tc.path, resp.StatusCode)
		}
	}
	if !engine.reached("POST /v1.47/networks/excalibase-proj-p1/connect") {
		t.Fatal("connect never reached the engine")
	}
}

func TestHandlerRefusesAppCallsOutsideTheScope(t *testing.T) {
	engine, proxy := appsProxyEngine(t)
	refused := []struct{ method, path, body string }{
		{http.MethodGet, "/v1.47/containers/json?all=1", ""},
		{http.MethodGet, "/v1.47/containers/excalibase-provisioning/logs?stdout=1", ""},
		{http.MethodPost, "/v1.47/containers/excalibase-provisioning/wait", ""},
		{http.MethodGet, "/v1.47/containers/excalibase-edge/logs", ""},
		{http.MethodPost, "/v1.47/containers/excalibase-edge/stop", ""},
		{http.MethodPost, "/v1.47/networks/create", string(networkCreateBody(t, func(b map[string]any) { b["Name"] = "excalibase-platform" }))},
		{http.MethodGet, "/v1.47/networks/excalibase-platform", ""},
		{http.MethodGet, "/v1.47/networks", ""},
		{http.MethodDelete, "/v1.47/networks/excalibase-tenants", ""},
		{http.MethodPost, "/v1.47/networks/excalibase-platform/connect", `{"Container":"excalibase-p1-postgres"}`},
		{http.MethodPost, "/v1.47/networks/excalibase-proj-p1/connect", `{"Container":"excalibase-provisioning"}`},
		{http.MethodPost, "/v1.47/networks/excalibase-proj-p1/connect", `{"Container":"excalibase-edge","EndpointConfig":{"Aliases":["studio"]}}`},
		{http.MethodPost, "/v1.47/networks/excalibase-proj-p1/disconnect", `{"Container":"excalibase-provisioning"}`},
		{http.MethodPost, "/v1.47/networks/prune", ""},
		{http.MethodPost, "/v1.47/volumes/create", `{"Name":"excalibase-platform-postgres","Labels":{"excalibase.managed":"true"}}`},
		{http.MethodGet, "/v1.47/volumes/excalibase-platform-secrets", ""},
		{http.MethodDelete, "/v1.47/volumes/excalibase-platform-postgres", ""},
		{http.MethodGet, "/v1.47/volumes", ""},
		{http.MethodPost, "/v1.47/volumes/prune", ""},
		{http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/update", `{"Memory":0}`},
	}
	for _, tc := range refused {
		resp := call(t, proxy, tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d, want refused", tc.method, tc.path, resp.StatusCode)
		}
	}
	for _, mutating := range []string{"POST /v1.47/networks/excalibase-proj-p1/disconnect", "DELETE /v1.47/volumes/excalibase-platform-postgres",
		"DELETE /v1.47/networks/excalibase-tenants", "POST /v1.47/containers/excalibase-edge/stop"} {
		if engine.reached(mutating) {
			t.Errorf("%s reached the engine", mutating)
		}
	}
}

// Without an edge container named, nothing outside the managed set is ever connected.
func TestHandlerConnectsNoUnmanagedContainerWithoutAnEdge(t *testing.T) {
	engine, proxy := newProxyWith(t, testPolicy())
	engine.containers["excalibase-edge"] = map[string]string{}
	resp := call(t, proxy.URL, http.MethodPost, "/v1.47/networks/excalibase-proj-p1/connect", `{"Container":"excalibase-edge"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("connect of an unmanaged container = %d, want 403", resp.StatusCode)
	}
}
