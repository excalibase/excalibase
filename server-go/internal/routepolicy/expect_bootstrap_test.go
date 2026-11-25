package routepolicy

import (
	"net/http"
	"testing"
)

func TestExpectCapabilityTokenOnBootstrapRoutes(t *testing.T) {
	bootstrap := Principal{Name: "svcBootstrap", PlatformAdmin: true,
		Capabilities: []string{"service-tokens:manage:svc-auth", "vault:init"}}
	service := Principal{Name: "svcAuth", PlatformAdmin: true, Capabilities: []string{"projects:info:read"}}
	bootstrapRow := Row{Pattern: "/api/admin/service-accounts/", Bootstrap: true}
	namedRow := Row{Pattern: "/api/projects/{projectId}/info", Capability: "projects:info:read"}
	plainRow := Row{Pattern: "/api/admin/tiers/{tier}"}

	cases := []struct {
		name string
		row  Row
		who  Principal
		want Expectation
	}{
		{"bootstrap token reaches a bootstrap route", bootstrapRow, bootstrap, Allow},
		{"another service token is refused on it", bootstrapRow, service, Deny403},
		{"a route naming a capability is not asserted", namedRow, service, Unasserted},
		{"a route naming none is refused", plainRow, bootstrap, Deny403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Expect(tc.row, http.MethodPost, tc.who); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
