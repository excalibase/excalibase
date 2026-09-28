package handler

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func TestAppCreateStoresItsInternalPorts(t *testing.T) {
	r, _ := setupAppRouter(t)
	body := validAppBody()
	body["internalPorts"] = []map[string]any{{"port": 6379, "protocol": "TCP"}}
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	want := []apphost.InternalPort{{Port: 6379, Protocol: "TCP"}}
	if got := decodeApp(t, w).InternalPorts; !reflect.DeepEqual(got, want) {
		t.Fatalf("internal ports = %+v, want %+v", got, want)
	}
}

func TestAppCreateRefusesABadInternalPort(t *testing.T) {
	r, _ := setupAppRouter(t)
	for _, ports := range [][]map[string]any{
		{{"port": 8080, "protocol": "TCP"}},
		{{"port": 6379, "protocol": "UDP"}},
		{{"port": 6379}},
		{{"port": 80, "protocol": "TCP"}},
	} {
		body := validAppBody()
		body["internalPorts"] = ports
		if w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body); w.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d, want 400", ports, w.Code)
		}
	}
}

func TestAppUpdateReplacesOrKeepsTheInternalPorts(t *testing.T) {
	r, _ := setupAppRouter(t)
	created := createAppForTest(t, r)
	path := "/api/projects/" + appTestProject + "/apps/" + created.ID + "/"

	w := doAppRequest(t, r, http.MethodPatch, path, map[string]any{"internalPorts": []map[string]any{{"port": 9092, "protocol": "TCP"}}})
	if w.Code != http.StatusOK || len(decodeApp(t, w).InternalPorts) != 1 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	w = doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"image": "ghcr.io/acme/storefront:1.5.0"}, 2)
	if got := decodeApp(t, w).InternalPorts; len(got) != 1 || got[0].Port != 9092 {
		t.Fatalf("an omitted field must keep the stored ports, got %+v", got)
	}
	w = doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"internalPorts": []map[string]any{}}, 3)
	if got := decodeApp(t, w).InternalPorts; len(got) != 0 {
		t.Fatalf("an empty list must clear the ports, got %+v", got)
	}
}
