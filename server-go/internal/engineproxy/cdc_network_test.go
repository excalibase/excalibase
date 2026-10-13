package engineproxy

import (
	"strings"
	"testing"
)

const watcherImage = "docker.io/excalibase/excalibase-watcher-go@sha256:7b2f9feb21b2a92b34c3f7b136399c619d460bf5d296215b8662a7a334126d9e"

func cdcPolicy() Policy {
	policy := testPolicy()
	policy.CDCNetwork = "excalibase-cdc"
	policy.CDCImages = []string{watcherImage}
	return policy
}

// watcherBody is what the provisioner sends for a project's CDC watcher: the
// tenants network to read its database, the bus network to publish, no ports.
func watcherBody(t *testing.T, mutate func(body map[string]any)) []byte {
	t.Helper()
	return createBody(t, func(body map[string]any) {
		body["Image"] = watcherImage
		hostConfig(body)["PortBindings"] = nil
		body["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{
			"excalibase-tenants": map[string]any{}, "excalibase-cdc": map[string]any{},
		}}
		if mutate != nil {
			mutate(body)
		}
	})
}

func TestCheckCreateAllowsTheWatcherOntoTheBusNetwork(t *testing.T) {
	if err := cdcPolicy().CheckCreate("excalibase-p1-watcher", watcherBody(t, nil)); err != nil {
		t.Fatalf("watcher create refused: %v", err)
	}
}

func TestCheckCreateKeepsEveryoneElseOffTheBusNetwork(t *testing.T) {
	cases := map[string]struct {
		policy Policy
		mutate func(body map[string]any)
		want   string
	}{
		"no bus network configured": {policy: testPolicy(), want: "excalibase-cdc"},
		"a tenant database":         {policy: cdcPolicy(), mutate: func(b map[string]any) { b["Image"] = "postgres:17" }, want: "may not join"},
		"the watcher's tag, not its digest": {policy: cdcPolicy(), want: "may not join",
			mutate: func(b map[string]any) { b["Image"] = "docker.io/excalibase/excalibase-watcher-go:1.5.1" }},
		"the watcher publishing a port": {policy: cdcPolicy(), want: "publish",
			mutate: func(b map[string]any) {
				hostConfig(b)["PortBindings"] = map[string]any{"8080/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}}}
			}},
		"the watcher image running another program": {policy: cdcPolicy(), want: "entrypoint",
			mutate: func(b map[string]any) { b["Entrypoint"] = []any{"/bin/sh"} }},
		"another image by network mode": {policy: cdcPolicy(), want: "network mode",
			mutate: func(b map[string]any) {
				b["Image"] = "postgres:17"
				hostConfig(b)["NetworkMode"] = "excalibase-cdc"
				b["NetworkingConfig"] = map[string]any{"EndpointsConfig": nil}
			}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.policy.CheckCreate("excalibase-p1-watcher", watcherBody(t, tc.mutate))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestTheBusNetworkIsNotAProjectNetwork(t *testing.T) {
	if cdcPolicy().networkAllowed("excalibase-cdc") {
		t.Fatal("the bus network must be admitted only through the watcher image")
	}
}
