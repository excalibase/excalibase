package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// fakeNodes is a platform with a fixed number of usable nodes.
type fakeNodes struct {
	nodes int
	err   error
	asked []string
}

func (f *fakeNodes) RequireNodeCount(_ context.Context, tier domain.TierType, instances int) error {
	f.asked = append(f.asked, fmt.Sprintf("%s:%d", tier, instances))
	if f.err != nil {
		return f.err
	}
	if instances > 1 && instances > f.nodes {
		return &service.NotEnoughNodesError{Tier: tier, Instances: instances, Nodes: f.nodes}
	}
	return nil
}

func (f *fakeNodes) RequireNodesForTier(ctx context.Context, tier domain.TierType) error {
	tc, _ := config.GetTierConfig(tier)
	return f.RequireNodeCount(ctx, tier, tc.Instances)
}

func TestProjectCreationRefusalsForNodePlacement(t *testing.T) {
	cases := map[string]struct {
		err  error
		code int
	}{
		"too few nodes":    {&service.NotEnoughNodesError{Tier: domain.Standard, Instances: 3, Nodes: 1}, http.StatusConflict},
		"nodes unreadable": {fmt.Errorf("%w: down", service.ErrNodePlacementUnknown), http.StatusServiceUnavailable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if !writeProjectCreationError(w, c.err) || w.Code != c.code {
				t.Fatalf("got %d, want %d", w.Code, c.code)
			}
			if strings.Contains(w.Body.String(), "down") {
				t.Errorf("the lookup's own error must stay in the log: %s", w.Body.String())
			}
		})
	}
}

func patchOrgTierWith(t *testing.T, nodes *fakeNodes, tier string) *httptest.ResponseRecorder {
	t.Helper()
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org-plan", domain.Free)
	h := NewOrgHandler(orgs, nil)
	if nodes != nil {
		h.SetNodePlacement(nodes)
	}
	r := chi.NewRouter()
	r.Patch("/orgs/{orgId}", h.UpdateOrg)
	req := httptest.NewRequest("PATCH", "/orgs/org-plan", strings.NewReader(`{"tier":"`+tier+`"}`))
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "admin-1", Role: "platform_admin", Active: true}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestUpdateOrgTier_RefusedWhenThePlatformCannotSpreadThePlan(t *testing.T) {
	w := patchOrgTierWith(t, &fakeNodes{nodes: 1}, "STANDARD")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "own node") {
		t.Fatalf("got %d %s, want 409 naming the node requirement", w.Code, w.Body.String())
	}
	if w := patchOrgTierWith(t, &fakeNodes{nodes: 3}, "STANDARD"); w.Code != http.StatusOK {
		t.Fatalf("3 nodes take the standard plan: got %d %s", w.Code, w.Body.String())
	}
}

func TestUpdateOrgTier_RefusedWithoutANodeCheck(t *testing.T) {
	if w := patchOrgTierWith(t, nil, "STANDARD"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unchecked plan change must be refused: got %d %s", w.Code, w.Body.String())
	}
}

func TestTierHandler_Update_RefusesMoreInstancesThanNodes(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
	h := NewTierHandler(store)
	nodes := &fakeNodes{nodes: 3}
	h.SetNodePlacement(nodes)
	body := `{"instances":5,"cpu":"2","memory":"4Gi","storageSize":"50Gi"}`

	rec := httptest.NewRecorder()
	h.Update(rec, newTierReqWithParam("PUT", body, "STANDARD"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d %s, want 409", rec.Code, rec.Body.String())
	}
	if _, stored := store.m[domain.Standard]; stored {
		t.Error("a refused spec must not be stored")
	}
}
