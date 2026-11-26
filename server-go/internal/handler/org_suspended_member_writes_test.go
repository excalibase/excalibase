package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type memberLookupStore struct {
	storage.OrgStore
	members map[string]bool
	err     error
}

func (s *memberLookupStore) GetOrgMember(_ context.Context, _, userID string) (*domain.OrgMember, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.members[userID] {
		return &domain.OrgMember{UserID: userID, Role: domain.OrgRoleAdmin}, nil
	}
	return nil, nil
}

func gatedOrgRouter(guard *orgguard.Guard) (http.Handler, *int) {
	return gatedOrgRouterOver(guard, &memberLookupStore{members: map[string]bool{"member": true}})
}

func gatedOrgRouterOver(guard *orgguard.Guard, store *memberLookupStore) (http.Handler, *int) {
	reached := 0
	h := NewOrgHandler(store, nil)
	h.SetOrgGuard(guard)
	r := chi.NewRouter()
	r.Route("/{orgId}", func(r chi.Router) {
		r.Use(h.refuseSuspendedMemberWrites)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) {
			reached++
			w.WriteHeader(http.StatusNoContent)
		})
	})
	return r, &reached
}

func callAs(router http.Handler, method, userID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/org1/x", strings.NewReader("{}"))
	if userID != "" {
		req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: userID}))
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestSuspendedOrgGate_RefusesAMembersWrite(t *testing.T) {
	router, reached := gatedOrgRouter(suspendedOrgGuard("org1"))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
		w := callAs(router, method, "member")
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), orgguard.Message) {
			t.Errorf("%s: %d %s, want 403 with the suspension message", method, w.Code, w.Body.String())
		}
	}
	if *reached != 0 {
		t.Fatalf("%d refused writes reached the handler", *reached)
	}
}

func TestSuspendedOrgGate_LetsReadsAndDeletesThrough(t *testing.T) {
	router, reached := gatedOrgRouter(suspendedOrgGuard("org1"))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodDelete} {
		if w := callAs(router, method, "member"); w.Code != http.StatusNoContent {
			t.Errorf("%s: %d, want it let through", method, w.Code)
		}
	}
	if *reached != 4 {
		t.Fatalf("%d requests reached the handler, want 4", *reached)
	}
}

func TestSuspendedOrgGate_LeavesANonMemberToTheHandlers404(t *testing.T) {
	router, reached := gatedOrgRouter(suspendedOrgGuard("org1"))
	w := callAs(router, http.MethodPatch, "stranger")
	if w.Code != http.StatusNoContent || *reached != 1 || strings.Contains(w.Body.String(), orgguard.Message) {
		t.Fatalf("a non-member got %d (reached %d); the gate must not say anything about the org", w.Code, *reached)
	}
}

func TestSuspendedOrgGate_AllowsAnOrgInGoodStanding(t *testing.T) {
	router, _ := gatedOrgRouter(suspendedOrgGuard())
	if w := callAs(router, http.MethodPatch, "member"); w.Code != http.StatusNoContent {
		t.Fatalf("got %d, want the write let through", w.Code)
	}
}

func TestSuspendedOrgGate_RefusesWhenTheStandingCannotBeRead(t *testing.T) {
	failing := orgguard.New(nil, func(context.Context) (map[string]bool, error) { return nil, errors.New("db down") })
	router, reached := gatedOrgRouter(failing)
	w := callAs(router, http.MethodPatch, "member")
	if w.Code != http.StatusServiceUnavailable || *reached != 0 {
		t.Fatalf("got %d (reached %d), want 503 and the write held back", w.Code, *reached)
	}
}

func TestSuspendedOrgGate_WithoutAGuardAWriteIsHeldBack(t *testing.T) {
	router, reached := gatedOrgRouter(nil)
	if w := callAs(router, http.MethodPatch, "member"); w.Code != http.StatusServiceUnavailable || *reached != 0 {
		t.Fatalf("no guard: %d (reached %d), want 503 and the write held back", w.Code, *reached)
	}
	if w := callAs(router, http.MethodGet, "member"); w.Code != http.StatusNoContent {
		t.Fatalf("no guard, read: %d, want it let through", w.Code)
	}
}

func TestSuspendedOrgGate_AnUnauthenticatedCallerIsLeftToTheAuthGate(t *testing.T) {
	router, _ := gatedOrgRouter(suspendedOrgGuard("org1"))
	if w := callAs(router, http.MethodPatch, ""); w.Code != http.StatusNoContent {
		t.Fatalf("no user: %d", w.Code)
	}
}

func TestSuspendedOrgGate_TellsNoRowsFromAFailedMembershipRead(t *testing.T) {
	noRows := &memberLookupStore{err: fmt.Errorf("org member not found: %w", sql.ErrNoRows)}
	router, reached := gatedOrgRouterOver(suspendedOrgGuard("org1"), noRows)
	if w := callAs(router, http.MethodPatch, "member"); w.Code != http.StatusNoContent || *reached != 1 {
		t.Fatalf("no rows: %d (reached %d), want the non-member left to the handler", w.Code, *reached)
	}
	broken := &memberLookupStore{err: errors.New("db down")}
	router, reached = gatedOrgRouterOver(suspendedOrgGuard("org1"), broken)
	if w := callAs(router, http.MethodPatch, "member"); w.Code != http.StatusServiceUnavailable || *reached != 0 {
		t.Fatalf("read failed: %d (reached %d), want 503 and the write held back", w.Code, *reached)
	}
}
