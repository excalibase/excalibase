package engineproxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Go's encoding/json (Docker) and Podman's compat API match keys without case and keep the
// last duplicate, so a key the proxy checks can carry a twin the engine reads instead (EXC-681).

const tenantCreate = "/v1.47/containers/create?name=excalibase-p1-postgres"

// createWithHost is a tenant create whose HostConfig is the raw JSON given.
func createWithHost(hostConfig string) string {
	return `{"Image":"postgres:17","Labels":{"excalibase.managed":"true"},"HostConfig":` + hostConfig +
		`,"NetworkingConfig":{"EndpointsConfig":{"excalibase-tenants":{}}}}`
}

func TestHandlerRefusesKeysRepeatedInAnotherCase(t *testing.T) {
	cases := map[string]struct{ path, body string }{
		"mount type twin turns tmpfs into a host bind": {tenantCreate,
			createWithHost(`{"Mounts":[{"Type":"tmpfs","type":"bind","Source":"/etc","Target":"/hostetc"}]}`)},
		"port binding twin publishes on every address": {tenantCreate,
			createWithHost(`{"PortBindings":{"5432/tcp":[{"HostIp":"127.0.0.1","hostip":"0.0.0.0"}]}}`)},
		"host port twin leaves the tenant range": {tenantCreate,
			createWithHost(`{"PortBindings":{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"","hostPort":"22"}]}}`)},
		"log driver twin": {tenantCreate, createWithHost(`{"LogConfig":{"Type":"json-file","TYPE":"syslog"}}`)},
		"Go folds the long s into S": {tenantCreate,
			createWithHost(`{"Mounts":[{"Type":"tmpfs","Target":"/x","Source":"","ſource":"/etc"}]}`)},
		"Go folds the Kelvin sign into k": {tenantCreate, `{"Image":"postgres:17","Labels":{"excalibase.managed":"true"},"HostConfig":{},` +
			`"NetworkingConfig":{"EndpointsConfig":{"excalibase-tenants":{}}},"Networ\u212aingConfig":{"EndpointsConfig":{"excalibase-platform":{}}}}`},
		"top-level twin": {tenantCreate, `{"Image":"postgres:17","image":"evil","Labels":{"excalibase.managed":"true"},"HostConfig":{}}`},
		"exec privileged twin": {"/v1.47/containers/excalibase-p1-postgres/exec",
			`{"Cmd":["sh"],"privileged":true,"Privileged":false}`},
		"network create twin": {"/v1.47/networks/create",
			`{"Name":"excalibase-proj-a","Labels":{"excalibase.managed":"true"},"Driver":"bridge","driver":"macvlan"}`},
		"volume create twin": {"/v1.47/volumes/create",
			`{"Name":"excalibase-proj-a","Labels":{"excalibase.managed":"true"},"Driver":"local","DRIVER":"nfs"}`},
		"network connect twin": {"/v1.47/networks/excalibase-proj-a/connect",
			`{"Container":"excalibase-p1-postgres","container":"excalibase-provisioning"}`},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			engine, proxy := newProxy(t)
			resp := call(t, proxy.URL, http.MethodPost, testCase.path, testCase.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("= %d, want 403", resp.StatusCode)
			}
			if engine.reached("POST " + strings.SplitN(testCase.path, "?", 2)[0]) {
				t.Fatal("the body reached the engine")
			}
		})
	}
}

// Podman reads "İ" as "i" where Go does not, and sorted re-encoding would put the twin last.
func TestHandlerRefusesKeysThatAreNotASCII(t *testing.T) {
	cases := map[string]struct{ path, body string }{
		"host config twin": {tenantCreate, `{"Image":"postgres:17","Labels":{"excalibase.managed":"true"},"HostConfig":{},` +
			`"HostConfİg":{"Privileged":true,"Binds":["/:/host"]}}`},
		"exec":         {"/v1.47/containers/excalibase-p1-postgres/exec", `{"Cmd":["sh"],"Privİleged":true}`},
		"port binding": {tenantCreate, createWithHost(`{"PortBindings":{"5432/tcp":[{"HostIp":"127.0.0.1","Hostİp":""}]}}`)},
		"network create": {"/v1.47/networks/create", `{"Name":"excalibase-proj-a","Labels":{"excalibase.managed":"true"},` +
			`"Driver":"bridge","Internal":true,"İnternal":false}`},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, proxy := newProxy(t)
			if resp := call(t, proxy.URL, http.MethodPost, testCase.path, testCase.body); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("= %d, want 403", resp.StatusCode)
			}
		})
	}
}

// An empty list in another case still unmasks /proc on the engine.
func TestCheckCreateRefusesAPIFieldsInAnotherCaseEvenEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"masked paths":      createWithHost(`{"maskedpaths":[]}`),
		"readonly paths":    createWithHost(`{"ReadonlyPATHS":[]}`),
		"mount bind option": createWithHost(`{"Mounts":[{"Type":"tmpfs","Target":"/x","bindoptions":{}}]}`),
		"endpoint":          `{"Image":"postgres:17","Labels":{"excalibase.managed":"true"},"HostConfig":{},"NetworkingConfig":{"EndpointsConfig":{"excalibase-tenants":{"aliases":[]}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := testPolicy().CheckCreate("excalibase-p1-postgres", []byte(body)); err == nil {
				t.Fatal("admitted")
			}
		})
	}
}

func TestCheckCreateRefusesAMapFieldWithEmptyValues(t *testing.T) {
	if err := testPolicy().CheckCreate("excalibase-p1-postgres", []byte(createWithHost(`{"Sysctls":{"kernel.x":""}}`))); err == nil {
		t.Fatal("admitted")
	}
}

func TestNetworkMembersRefuseFieldsInAnotherCase(t *testing.T) {
	policy := testPolicy()
	if err := policy.CheckNetworkConnect([]byte(`{"Container":"excalibase-p1-postgres","endpointconfig":{}}`)); err == nil {
		t.Fatal("connect admitted")
	}
	if _, err := networkMember([]byte(`{"Container":"excalibase-p1-postgres","force":false}`), true); err == nil {
		t.Fatal("disconnect admitted")
	}
	if err := policy.CheckVolumeCreate([]byte(`{"Name":"excalibase-proj-a","Labels":{"excalibase.managed":"true"},"driverOpts":{}}`)); err == nil {
		t.Fatal("volume create admitted")
	}
}

func TestJSONFieldNamesFollowEmbeddedStructsAndTags(t *testing.T) {
	for _, want := range []string{"Image", "Entrypoint", "HostConfig", "NetworkingConfig"} {
		if !slices.Contains(createRequestNames, want) {
			t.Errorf("create names miss %s", want)
		}
	}
	for _, want := range []string{"CpuShares", "Memory", "MaskedPaths", "Binds"} {
		if !slices.Contains(hostConfigNames, want) {
			t.Errorf("HostConfig names miss %s", want)
		}
	}
	if !slices.Contains(portBindingNames, "HostIp") || slices.Contains(portBindingNames, "HostIP") {
		t.Errorf("port binding names %v", portBindingNames)
	}
}

func TestHandlerRefusesExactDuplicateKeys(t *testing.T) {
	_, proxy := newProxy(t)
	body := createWithHost(`{"Binds":["/:/host"],"Binds":[]}`)
	if resp := call(t, proxy.URL, http.MethodPost, tenantCreate, body); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("= %d, want 403", resp.StatusCode)
	}
}

// Lone keys in another case are what the engine reads, so where the proxy looks a field up by
// name it must find it by that name or refuse.
func TestCheckCreateRefusesInspectedFieldsSpelledAnotherWay(t *testing.T) {
	cases := map[string]string{
		"log driver":           createWithHost(`{"LogConfig":{"type":"syslog"}}`),
		"volume driver":        createWithHost(`{"Mounts":[{"Type":"volume","Source":"excalibase-proj-a","Target":"/d","volumeOptions":{"DriverConfig":{"Name":"nfs"}}}]}`),
		"volume driver config": createWithHost(`{"Mounts":[{"Type":"volume","Source":"excalibase-proj-a","Target":"/d","VolumeOptions":{"driverconfig":{"Name":"nfs"}}}]}`),
		"bind options":         createWithHost(`{"Mounts":[{"Type":"volume","Source":"excalibase-proj-a","Target":"/d","BindOptions":{"Propagation":"rshared"}}]}`),
		"host config":          `{"Image":"postgres:17","Labels":{"excalibase.managed":"true"},"hostconfig":{}}`,
		"networking config":    `{"Image":"postgres:17","Labels":{"excalibase.managed":"true"},"HostConfig":{},"networkingConfig":{"EndpointsConfig":{"excalibase-platform":{}}}}`,
		"endpoints config":     `{"Image":"postgres:17","Labels":{"excalibase.managed":"true"},"HostConfig":{},"NetworkingConfig":{"endpointsconfig":{"excalibase-platform":{}}}}`,
		"image":                `{"IMAGE":"postgres:17","Labels":{"excalibase.managed":"true"},"HostConfig":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := testPolicy().CheckCreate("excalibase-p1-postgres", []byte(body)); err == nil {
				t.Fatal("admitted")
			}
		})
	}
}

func TestCheckExecCreateRefusesPrivilegedSpelledAnotherWay(t *testing.T) {
	if err := CheckExecCreate([]byte(`{"Cmd":["sh"],"privileged":true}`)); err == nil {
		t.Fatal("admitted")
	}
}

func TestHandlerRefusesBodiesThatAreNotOneJSONValue(t *testing.T) {
	for name, body := range map[string]string{
		"trailing value": `{"Cmd":["true"]}{"Privileged":true}`,
		"empty":          ``,
		"too deep":       nested(maxBodyDepth),
		"not an object":  `null`,
	} {
		t.Run(name, func(t *testing.T) {
			_, proxy := newProxy(t)
			resp := call(t, proxy.URL, http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/exec", body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("= %d, want 403", resp.StatusCode)
			}
		})
	}
}

// nested is an exec body whose deepest value sits depth levels below the top.
func nested(depth int) string {
	return `{"Cmd":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `}`
}

func TestHandlerTakesBodiesNestedUpToTheLimit(t *testing.T) {
	_, proxy := newProxy(t)
	resp := call(t, proxy.URL, http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/exec", nested(maxBodyDepth-1))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= %d, want forwarded", resp.StatusCode)
	}
}

// The engine reads the proxy's own encoding of what it checked, never the caller's bytes.
func TestHandlerForwardsItsOwnEncodingOfTheCheckedBody(t *testing.T) {
	_, proxy := newProxy(t)
	sent := " {\"Cmd\" : [\"sh\", \"-c\", \"a && b < c\"],\n \"Tty\":false, \"ConsoleSize\":[24,80], \"Env\":[\"N=1\"], \"X\":9007199254740993, \"Y\":1.0} "
	resp := call(t, proxy.URL, http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/exec", sent)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= %d", resp.StatusCode)
	}
	want := `{"Cmd":["sh","-c","a && b < c"],"ConsoleSize":[24,80],"Env":["N=1"],"Tty":false,"X":9007199254740993,"Y":1.0}`
	if got := resp.Header.Get("X-Echo-Body"); got != want {
		t.Fatalf("engine got %s\nwant %s", got, want)
	}
}

func TestHandlerForwardsTheEdgeConnectReencoded(t *testing.T) {
	policy := testPolicy()
	policy.EdgeContainer, policy.EdgeAliases = "excalibase-edge", []string{"api.example.com"}
	_, proxy := newProxyWith(t, policy)
	sent := `{"Container": "excalibase-edge", "EndpointConfig": {"Aliases": ["api.example.com"]}}`
	resp := call(t, proxy.URL, http.MethodPost, "/v1.47/networks/excalibase-proj-a/connect", sent)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(resp.Header.Get("X-Echo-Body")), &got); err != nil || got["Container"] != "excalibase-edge" {
		t.Fatalf("engine got %v (%v)", got, err)
	}
}

func TestFoldNameMatchesGo(t *testing.T) {
	type probe struct{ Source, Kind string }
	var decoded probe
	if err := json.Unmarshal([]byte(`{"ſource":"a","\u212aind":"b"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Source != "a" || decoded.Kind != "b" {
		t.Fatalf("Go did not fold as assumed: %+v", decoded)
	}
	for _, pair := range [][2]string{{"ſource", "Source"}, {"Kind", "KIND"}, {"HostIp", "hostip"}} {
		if foldName(pair[0]) != foldName(pair[1]) {
			t.Errorf("%q and %q fold apart", pair[0], pair[1])
		}
	}
	// Go folds by simple case folding only: dotless i has no orbit.
	if foldName("ımage") == foldName("Image") || foldName("HostIp") == foldName("HostPort") {
		t.Fatal("distinct names fold together")
	}
}

func TestManagedListFilterRefusesARepeatedKey(t *testing.T) {
	_, proxy := newProxy(t)
	filters := `{"label":["x=y"],"label":["excalibase.managed=true"]}`
	resp := call(t, proxy.URL, http.MethodGet, "/v1.47/containers/json?filters="+url.QueryEscape(filters), "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("= %d, want 403", resp.StatusCode)
	}
}
