package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

type fakeRestarts struct {
	byApp map[string]k8s.AppRestarts
	err   error
}

func (f fakeRestarts) AppRestarts(_ context.Context, _, appID string) (k8s.AppRestarts, error) {
	return f.byApp[appID], f.err
}

func restartsRouter(t *testing.T, restarts AppRestartReader) (chi.Router, *fakeAppStore) {
	t.Helper()
	store := newFakeAppStore()
	h := NewAppHandler(store, newFakeSources("storefront_db"), testAppRoute)
	h.SetFeatures(features.NewStatic(features.All()...))
	h.SetAppLimits(fakeAppLimits{limit: testAppLimit})
	h.SetRestarts(restarts)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps", func(r chi.Router) { h.Routes(r) })
	return r, store
}

type restartsView struct {
	ID       string           `json:"id"`
	Restarts *k8s.AppRestarts `json:"restarts"`
}

// EXC-651: an app that was OOMKilled and restarted read ACTIVE in the list and
// on its own page; both carry the restarts and the last stop's reason.
func TestAppsCarryTheirRestartsAndLastStopReason(t *testing.T) {
	stopped := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	exit := int32(137)
	restarts := fakeRestarts{byApp: map[string]k8s.AppRestarts{}}
	r, _ := restartsRouter(t, restarts)
	created := createAppForTest(t, r)
	restarts.byApp[created.ID] = k8s.AppRestarts{Count: 3, LastReason: "OOMKilled", LastExitCode: &exit, LastAt: &stopped}

	w := doAppRequest(t, r, http.MethodGet, "/api/projects/"+appTestProject+"/apps/", nil)
	var listed []restartsView
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("list = %s (%v)", w.Body.String(), err)
	}
	w = doAppRequest(t, r, http.MethodGet, "/api/projects/"+appTestProject+"/apps/"+created.ID+"/", nil)
	var one restartsView
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	for name, view := range map[string]restartsView{"list": listed[0], "get": one} {
		if view.Restarts == nil || view.Restarts.Count != 3 || view.Restarts.LastReason != "OOMKilled" {
			t.Errorf("%s restarts = %+v, want 3 and OOMKilled", name, view.Restarts)
		}
	}
}

// A runtime that cannot answer leaves the field out; the app still lists.
func TestAppsListWhenTheirRestartsCannotBeRead(t *testing.T) {
	r, _ := restartsRouter(t, fakeRestarts{err: errors.New("cluster down")})
	createAppForTest(t, r)
	w := doAppRequest(t, r, http.MethodGet, "/api/projects/"+appTestProject+"/apps/", nil)
	var listed []restartsView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].Restarts != nil {
		t.Fatalf("list = %d %s, want the app without restarts", w.Code, w.Body.String())
	}
}
