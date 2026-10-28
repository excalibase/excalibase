package cfworkers

import (
	"context"
	"net/http"
	"testing"
)

func routerSettings(bindings ...map[string]any) map[string]any {
	return map[string]any{"bindings": bindings}
}

func dispatchBinding(namespace, outbound string) map[string]any {
	binding := map[string]any{"type": "dispatch_namespace", "name": "SITES", "namespace": namespace}
	if outbound != "" {
		binding["outbound"] = map[string]any{"worker": map[string]any{"service": outbound}, "params": []any{map[string]any{"name": "siteId"}}}
	}
	return binding
}

// EXC-661: server code runs only behind the outbound Worker the router's
// binding of this namespace names (binding shape unverified on Cloudflare: R5).
func TestDispatchOutboundReadsTheRoutersBindingOfThisNamespace(t *testing.T) {
	fake, client := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeEnvelope(w, http.StatusOK, routerSettings(
			map[string]any{"type": "plain_text", "name": "BASE_DOMAIN", "text": "excalibase.app"},
			dispatchBinding("another-ns", "other-outbound"),
			dispatchBinding(testNamespace, "excalibase-sites-outbound")))
	})
	got, err := client.DispatchOutbound(context.Background(), "excalibase-sites-router")
	if err != nil || got != "excalibase-sites-outbound" {
		t.Fatalf("outbound = %q err=%v", got, err)
	}
	request := fake.requests[0]
	if request.method != http.MethodGet || request.path != "/client/v4/accounts/"+testAccount+"/workers/scripts/excalibase-sites-router/settings" {
		t.Fatalf("request = %s %s", request.method, request.path)
	}
}

func TestDispatchOutboundIsEmptyWithoutAnOutboundOnThisNamespace(t *testing.T) {
	for name, settings := range map[string]map[string]any{
		"no outbound":         routerSettings(dispatchBinding(testNamespace, "")),
		"only another's":      routerSettings(dispatchBinding("another-ns", "excalibase-sites-outbound")),
		"no dispatch binding": routerSettings(),
	} {
		_, client := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeEnvelope(w, http.StatusOK, settings) })
		if got, err := client.DispatchOutbound(context.Background(), "excalibase-sites-router"); err != nil || got != "" {
			t.Errorf("%s: outbound = %q err=%v", name, got, err)
		}
	}
}

func TestDispatchOutboundReportsARefusalAndABadName(t *testing.T) {
	_, client := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeEnvelope(w, http.StatusNotFound, nil) })
	if _, err := client.DispatchOutbound(context.Background(), "excalibase-sites-router"); err == nil {
		t.Error("a missing router was not reported")
	}
	if _, err := client.DispatchOutbound(context.Background(), "../x"); err == nil {
		t.Error("a bad router name reached Cloudflare")
	}
}
