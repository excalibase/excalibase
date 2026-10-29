package engineproxy

import (
	"strings"
	"testing"
)

// appCreate is a hosted app joining its project network under its one service name.
func appCreate(t *testing.T, mutate func(host, endpoint map[string]any, b map[string]any)) []byte {
	t.Helper()
	return createBody(t, func(b map[string]any) {
		host := hostConfig(b)
		host["NetworkMode"] = "excalibase-proj-p1"
		host["PortBindings"] = nil
		host["DnsSearch"] = []any{AppServiceDomain}
		host["DnsOptions"] = []any{"ndots:1"}
		endpoint := map[string]any{"Aliases": []any{"web." + AppServiceDomain}, "IPAddress": "", "Links": nil}
		b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-proj-p1": endpoint}}
		if mutate != nil {
			mutate(host, endpoint, b)
		}
	})
}

func TestCheckCreateAllowsAnAppsServiceName(t *testing.T) {
	if err := testPolicy().CheckCreate("excalibase-app-web", appCreate(t, nil)); err != nil {
		t.Fatalf("app with its service name refused: %v", err)
	}
}

func TestCheckCreateRefusesEveryOtherName(t *testing.T) {
	policy := testPolicy()
	policy.EdgeAliases = []string{"api." + AppServiceDomain}
	cases := map[string]func(host, endpoint, b map[string]any){
		"a bare name": func(_, e, _ map[string]any) { e["Aliases"] = []any{"provisioning"} },
		"a platform host": func(_, e, _ map[string]any) {
			e["Aliases"] = []any{"api." + AppServiceDomain}
		},
		"two labels":    func(_, e, _ map[string]any) { e["Aliases"] = []any{"a.b." + AppServiceDomain} },
		"an upper case": func(_, e, _ map[string]any) { e["Aliases"] = []any{"Web." + AppServiceDomain} },
		"two aliases": func(_, e, _ map[string]any) {
			e["Aliases"] = []any{"web." + AppServiceDomain, "db." + AppServiceDomain}
		},
		"another domain": func(_, e, _ map[string]any) { e["Aliases"] = []any{"web.internal"} },
		"a static address beside it": func(_, e, _ map[string]any) {
			e["IPAMConfig"] = map[string]any{"IPv4Address": "10.0.0.2"}
		},
		"on the shared tenants network": func(h, e, b map[string]any) {
			h["NetworkMode"] = "excalibase-tenants"
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-tenants": e}}
		},
		"another search domain": func(h, _, _ map[string]any) { h["DnsSearch"] = []any{"excalibase.io"} },
		"two search domains":    func(h, _, _ map[string]any) { h["DnsSearch"] = []any{AppServiceDomain, "corp"} },
		"other dns options":     func(h, _, _ map[string]any) { h["DnsOptions"] = []any{"ndots:5", "rotate"} },
		"own name servers":      func(h, _, _ map[string]any) { h["Dns"] = []any{"8.8.8.8"} },
		"a platform container's name": func(_, e, _ map[string]any) {
			e["Aliases"] = []any{"excalibase-p1-postgres." + AppServiceDomain}
		},
		"a platform host in another case": func(_, e, _ map[string]any) {
			e["Aliases"] = []any{"API." + AppServiceDomain}
		},
		"a second network": func(_, e, b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-proj-p1": e, "excalibase-proj-p2": map[string]any{}}}
		},
		"another project's network": func(_, e, b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-proj-p2": e}}
		},
	}
	for label, mutate := range cases {
		t.Run(label, func(t *testing.T) {
			err := policy.CheckCreate("excalibase-app-web", appCreate(t, mutate))
			if err == nil {
				t.Fatal("create allowed")
			}
			if !strings.Contains(err.Error(), "may") && !strings.Contains(err.Error(), "not") {
				t.Fatalf("error %q", err)
			}
		})
	}
}
