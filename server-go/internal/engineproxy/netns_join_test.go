package engineproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	gatewayImage = "ghcr.io/documentdb/documentdb-kubernetes-operator/gateway@sha256:8a5c7e533dc0fbed8ed780b22e31001a6b12ae0eb670f4aac62c1ded782742ed"
	otherImage   = "docker.io/library/alpine@sha256:0a4eaa0eecf5f8c050e5bba433f58c052be7587ee8af3e8b3910ef9ab5fbe9f5"
)

func joinPolicy() Policy {
	policy := testPolicy()
	policy.NetnsJoinImages = []string{gatewayImage}
	return policy
}

// gatewayBody is what the provisioner sends for a DocumentDB gateway: the
// database container's network namespace, no ports, no networks of its own.
func gatewayBody(t *testing.T, mutate func(body map[string]any)) []byte {
	t.Helper()
	return createBody(t, func(body map[string]any) {
		body["Image"] = gatewayImage
		host := hostConfig(body)
		host["NetworkMode"] = "container:excalibase-p1-postgres"
		host["PortBindings"] = nil
		body["NetworkingConfig"] = map[string]any{"EndpointsConfig": nil}
		if mutate != nil {
			mutate(body)
		}
	})
}

func TestCheckCreateAllowsTheGatewayIntoAContainersNetworkNamespace(t *testing.T) {
	if err := joinPolicy().CheckCreate("excalibase-p1-documentdb", gatewayBody(t, nil)); err != nil {
		t.Fatalf("gateway create refused: %v", err)
	}
}

func TestCheckCreateRefusesNetnsJoins(t *testing.T) {
	cases := map[string]struct {
		policy Policy
		mutate func(body map[string]any)
		want   string
	}{
		"no images allowed": {policy: testPolicy(), want: "network mode"},
		"image not allowed": {policy: joinPolicy(), mutate: func(b map[string]any) { b["Image"] = otherImage }, want: "may not join"},
		"same repository, tag instead of digest": {policy: joinPolicy(), want: "may not join",
			mutate: func(b map[string]any) {
				b["Image"] = "ghcr.io/documentdb/documentdb-kubernetes-operator/gateway:0.117.0"
			}},
		"publishes a port": {policy: joinPolicy(), want: "publish",
			mutate: func(b map[string]any) {
				hostConfig(b)["PortBindings"] = map[string]any{"10260/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}}}
			}},
		"joins a network as well": {policy: joinPolicy(), want: "network",
			mutate: func(b map[string]any) {
				b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-tenants": map[string]any{}}}
			}},
		"empty target":      {policy: joinPolicy(), mutate: setMode("container:"), want: "container"},
		"path-like target":  {policy: joinPolicy(), mutate: setMode("container:../x"), want: "container"},
		"host network":      {policy: joinPolicy(), mutate: setMode("host"), want: "network mode"},
		"joins the pid ns":  {policy: joinPolicy(), mutate: setHost("PidMode", "container:excalibase-p1-postgres"), want: "PidMode"},
		"joins the ipc ns":  {policy: joinPolicy(), mutate: setHost("IpcMode", "container:excalibase-p1-postgres"), want: "IpcMode"},
		"privileged":        {policy: joinPolicy(), mutate: setHost("Privileged", true), want: "Privileged"},
		"adds a capability": {policy: joinPolicy(), mutate: setHost("CapAdd", []any{"NET_ADMIN"}), want: "CapAdd"},
		"mounts a host dir": {policy: joinPolicy(), mutate: setHost("Binds", []any{"/etc:/host-etc"}), want: "host path"},
		"unmanaged":         {policy: joinPolicy(), mutate: func(b map[string]any) { b["Labels"] = map[string]any{} }, want: "label"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.policy.CheckCreate("excalibase-p1-documentdb", gatewayBody(t, tc.mutate))
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal mentioning %q", err, tc.want)
			}
		})
	}
}

func TestCheckCreateRefusesTheAllowedImageOutsideANetnsJoin(t *testing.T) {
	// The allowlist only widens the network mode; the image is otherwise held
	// to every rule a tenant container is.
	body := createBody(t, func(b map[string]any) {
		b["Image"] = gatewayImage
		hostConfig(b)["NetworkMode"] = "host"
	})
	if err := joinPolicy().CheckCreate("excalibase-p1-documentdb", body); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want refused", err)
	}
}

func TestNetnsJoinTargetNamesTheJoinedContainer(t *testing.T) {
	if got := NetnsJoinTarget(gatewayBody(t, nil)); got != "excalibase-p1-postgres" {
		t.Fatalf("target = %q", got)
	}
	if got := NetnsJoinTarget(createBody(t, nil)); got != "" {
		t.Fatalf("a bridge container has target %q", got)
	}
}

func setMode(mode string) func(map[string]any) { return setHost("NetworkMode", mode) }

func setHost(field string, value any) func(map[string]any) {
	return func(body map[string]any) { hostConfig(body)[field] = value }
}

func newJoinProxy(t *testing.T) (*fakeEngine, *httptest.Server) {
	t.Helper()
	engine := &fakeEngine{containers: map[string]map[string]string{
		"excalibase-p1-postgres":  {"excalibase.managed": "true"},
		"excalibase-provisioning": {"com.docker.compose.service": "provisioning"},
		"excalibase-p2-postgres":  {"excalibase.managed": "false"},
	}}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(joinPolicy(), target, http.DefaultTransport).WithUpgradeDial(
		func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", target.Host)
		})
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)
	return engine, proxy
}

func TestHandlerForwardsAGatewayJoiningAManagedContainer(t *testing.T) {
	engine, proxy := newJoinProxy(t)
	resp := call(t, proxy.URL, http.MethodPost, "/v1.43/containers/create?name=excalibase-p1-documentdb", string(gatewayBody(t, nil)))
	if resp.StatusCode != http.StatusOK || !engine.reached("POST /v1.43/containers/create") {
		t.Fatalf("status %d, forwarded %v", resp.StatusCode, engine.forwarded)
	}
}

func TestHandlerRefusesAGatewayJoiningAnythingButAManagedContainer(t *testing.T) {
	cases := map[string]struct {
		target string
		status int
	}{
		"platform container":           {"excalibase-provisioning", http.StatusForbidden},
		"labelled other than true":     {"excalibase-p2-postgres", http.StatusForbidden},
		"container the engine lacks":   {"excalibase-p9-postgres", http.StatusNotFound},
		"another tenant's id, unknown": {"0123456789ab", http.StatusNotFound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			engine, proxy := newJoinProxy(t)
			body := gatewayBody(t, setMode("container:"+tc.target))
			resp := call(t, proxy.URL, http.MethodPost, "/v1.43/containers/create?name=excalibase-p1-documentdb", string(body))
			if resp.StatusCode != tc.status || engine.reached("POST /v1.43/containers/create") {
				t.Fatalf("status %d (want %d), forwarded %v", resp.StatusCode, tc.status, engine.forwarded)
			}
		})
	}
}

// A create that joins no namespace names no target, whatever its network mode.
func TestNetnsJoinTargetIsEmptyWithoutAJoin(t *testing.T) {
	for _, mode := range []string{"", "none", "bridge", "excalibase-tenants", "excalibase-net-p1"} {
		body := []byte(`{"HostConfig":{"NetworkMode":"` + mode + `"}}`)
		if target := NetnsJoinTarget(body); target != "" {
			t.Errorf("mode %q: target %q", mode, target)
		}
	}
	if target := NetnsJoinTarget([]byte(`{"HostConfig":{"NetworkMode":"container:excalibase-p1-postgres"}}`)); target != "excalibase-p1-postgres" {
		t.Errorf("join target %q", target)
	}
}
