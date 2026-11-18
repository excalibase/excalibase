package handler

import (
	"net/http"
	"strings"
	"testing"
)

const blockedRegistryMessage = "images from ttl.sh are not allowed; push to a registry such as Docker Hub or GHCR"

func TestAppCreate_RefusesAnImageFromABlockedRegistry(t *testing.T) {
	for _, image := range []string{"ttl.sh/abc:1h", "TTL.SH:443/abc:1h", "ttl.sh/abc@sha256:" + strings.Repeat("a", 64)} {
		r, store, audit := proxyGuardRouter(t, false)
		body := validAppBody()
		body["image"] = image
		w := doAppRequest(t, r, http.MethodPost, proxyAppsPath, body)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), blockedRegistryMessage) {
			t.Fatalf("%s: %d %s", image, w.Code, w.Body.String())
		}
		if apps, _ := store.List(appTestProject); len(apps) != 0 {
			t.Errorf("%s: a refused app was stored", image)
		}
		if len(audit.entries) != 1 {
			t.Errorf("%s: audit entries = %d, want 1", image, len(audit.entries))
		}
	}
}

func TestAppUpdate_RefusesAnImageFromABlockedRegistry(t *testing.T) {
	r, store, _ := proxyGuardRouter(t, false)
	created := doAppRequest(t, r, http.MethodPost, proxyAppsPath, validAppBody())
	if created.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", created.Code, created.Body.String())
	}
	id := decodeApp(t, created).ID

	w := doAppRequestWithVersion(t, r, http.MethodPatch, proxyAppsPath+id, map[string]any{"image": "ttl.sh/abc:1h"}, 1)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), blockedRegistryMessage) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if stored, _ := store.Get(appTestProject, id); stored.Image != "ghcr.io/acme/storefront:1.4.2" {
		t.Errorf("a refused update changed the image: %s", stored.Image)
	}
	for _, image := range []string{"nginx:1.27", "docker.io/ttl.sh-fan/foo:1", "registry.example.com:5000/x:1"} {
		if ok := doAppRequestWithVersion(t, r, http.MethodPatch, proxyAppsPath+id, map[string]any{"image": image}, decodeApp(t, doAppRequest(t, r, http.MethodGet, proxyAppsPath+id, nil)).Version); ok.Code != http.StatusOK {
			t.Errorf("%s: %d %s", image, ok.Code, ok.Body.String())
		}
	}
}
