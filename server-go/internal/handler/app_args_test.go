package handler

import (
	"net/http"
	"slices"
	"testing"
)

func TestAppCreateStoresItsArgs(t *testing.T) {
	r, _ := setupAppRouter(t)
	body := validAppBody()
	body["args"] = []string{"--appendonly", "yes"}
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if got := decodeApp(t, w).Args; !slices.Equal(got, []string{"--appendonly", "yes"}) {
		t.Fatalf("args = %v", got)
	}
}

func TestAppCreateRefusesAnEmptyArg(t *testing.T) {
	r, _ := setupAppRouter(t)
	body := validAppBody()
	body["args"] = []string{""}
	if w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body); w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

func TestAppUpdateReplacesOrKeepsTheArgs(t *testing.T) {
	r, _ := setupAppRouter(t)
	created := createAppForTest(t, r)
	path := "/api/projects/" + appTestProject + "/apps/" + created.ID + "/"

	w := doAppRequest(t, r, http.MethodPatch, path, map[string]any{"args": []string{"serve", "--verbose"}})
	if w.Code != http.StatusOK || len(decodeApp(t, w).Args) != 2 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	w = doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"image": "ghcr.io/acme/storefront:1.5.0"}, 2)
	if got := decodeApp(t, w).Args; len(got) != 2 {
		t.Fatalf("an omitted field must keep the stored args, got %v", got)
	}
	w = doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"args": []string{}}, 3)
	if got := decodeApp(t, w).Args; len(got) != 0 {
		t.Fatalf("an empty list must go back to the image's command, got %v", got)
	}
}
