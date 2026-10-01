package apptemplate

import (
	"strings"
	"testing"
)

func TestBuiltinsParseAndCoverTheStarters(t *testing.T) {
	catalog, err := Builtins()
	if err != nil {
		t.Fatalf("builtins: %v", err)
	}
	want := map[string]bool{"redis": false, "web-redis": false, "web-postgres": false, "storefront-demo": false}
	for _, tpl := range catalog.List() {
		if _, ok := want[tpl.ID]; ok {
			want[tpl.ID] = true
		}
		for _, app := range tpl.Apps {
			if !strings.Contains(app.Image, "@sha256:") {
				t.Errorf("%s/%s: a built-in image is pinned by digest, got %q", tpl.ID, app.Name, app.Image)
			}
		}
		if tpl.Source == "" {
			t.Errorf("%s: the source is kept for the details page", tpl.ID)
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("built-in %q is missing", id)
		}
	}
	if tpl, ok := catalog.Get("web-redis"); !ok || !tpl.Facts().NeedsPrivateNetwork || len(tpl.Apps) != 2 {
		t.Fatalf("web-redis: %+v", tpl)
	}
	if tpl, ok := catalog.Get("web-postgres"); !ok || !tpl.Facts().NeedsDatabase || tpl.Facts().NeedsPrivateNetwork {
		t.Fatalf("web-postgres: %+v", tpl)
	}
	if tpl, ok := catalog.Get("storefront-demo"); !ok || !tpl.Facts().NeedsPrivateNetwork || len(tpl.Apps) != 2 {
		t.Fatalf("storefront-demo: %+v", tpl)
	}
	if _, ok := catalog.Get("nope"); ok {
		t.Fatal("an unknown id must not resolve")
	}
}

// A built-in fits the smallest plan: FREE holds 2 apps and a 1Gi disk.
func TestBuiltinsFitTheFreePlan(t *testing.T) {
	catalog, err := Builtins()
	if err != nil {
		t.Fatalf("builtins: %v", err)
	}
	for _, tpl := range catalog.List() {
		if len(tpl.Apps) > 2 {
			t.Errorf("%s: %d apps is more than FREE holds", tpl.ID, len(tpl.Apps))
		}
		for _, app := range tpl.Apps {
			if app.Disk != nil && app.Disk.Size != "1Gi" && !strings.HasSuffix(app.Disk.Size, "Mi") {
				t.Errorf("%s/%s: disk %s is above FREE's 1Gi", tpl.ID, app.Name, app.Disk.Size)
			}
		}
	}
}

// The storefront demo runs the published demo image on 8080 with a health
// check, and reaches its Redis by name with the generated password.
func TestStorefrontDemoRunsThePublishedImage(t *testing.T) {
	catalog, err := Builtins()
	if err != nil {
		t.Fatalf("builtins: %v", err)
	}
	tpl, ok := catalog.Get("storefront-demo")
	if !ok {
		t.Fatal("storefront-demo is missing")
	}
	var store *AppSpec
	for i := range tpl.Apps {
		if tpl.Apps[i].Name == "storefront" {
			store = &tpl.Apps[i]
		}
	}
	if store == nil {
		t.Fatal("storefront-demo has no storefront app")
	}
	if !strings.HasPrefix(store.Image, "excalibase/storefront-demo@sha256:") || store.Port != 8080 || store.HealthCheckPath != "/healthz" || store.Internal {
		t.Fatalf("storefront app: %+v", store)
	}
	var redisURL string
	for _, variable := range store.Env {
		if variable.Name == "REDIS_URL" {
			redisURL = variable.Value
		}
	}
	if !strings.Contains(redisURL, "${{ apps.redis.env.REDIS_PASSWORD }}") || !strings.Contains(redisURL, "${{ apps.redis.host }}") {
		t.Fatalf("REDIS_URL: %q", redisURL)
	}
}
