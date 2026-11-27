package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

type fakeBudgetSource struct {
	report planbudget.Report
	err    error
	asked  string
}

func (f *fakeBudgetSource) PlanBudgetReport(_ context.Context, orgID string) (planbudget.Report, error) {
	f.asked = orgID
	return f.report, f.err
}

type onlyMember struct{ orgID, userID string }

func (m onlyMember) GetOrgMember(_ context.Context, orgID, userID string) (*domain.OrgMember, error) {
	if orgID == m.orgID && userID == m.userID {
		return &domain.OrgMember{OrgID: orgID, UserID: userID}, nil
	}
	return nil, errors.New("not a member")
}

func budgetRouter(h *PlanBudgetHandler) http.Handler {
	r := chi.NewRouter()
	r.Route("/orgs/{orgId}", func(r chi.Router) { h.OrgRoutes(r, func(next http.Handler) http.Handler { return next }) })
	return r
}

func getBudget(t *testing.T, h http.Handler, orgID, userID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/orgs/"+orgID+"/plan-budget", nil)
	if userID != "" {
		req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: userID}))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestAMemberReadsTheirOrgsBudget(t *testing.T) {
	source := &fakeBudgetSource{report: planbudget.Report{Enabled: true, Plan: "FREE", Budget: planbudget.Size{CPUMilli: 1000}, Services: []planbudget.ServiceUse{}}}
	w := getBudget(t, budgetRouter(NewPlanBudgetHandler(source, onlyMember{"o1", "u1"})), "o1", "u1")
	if w.Code != http.StatusOK || source.asked != "o1" {
		t.Fatalf("%d %s asked=%q", w.Code, w.Body.String(), source.asked)
	}
	var body planbudget.Report
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Budget.CPUMilli != 1000 || !body.Enabled {
		t.Fatalf("%v %+v", err, body)
	}
}

func TestANonMemberOrAnonymousCallerReadsNothing(t *testing.T) {
	source := &fakeBudgetSource{}
	h := budgetRouter(NewPlanBudgetHandler(source, onlyMember{"o1", "u1"}))
	if w := getBudget(t, h, "o1", "stranger"); w.Code != http.StatusNotFound {
		t.Fatalf("a stranger: %d", w.Code)
	}
	if w := getBudget(t, h, "o1", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if source.asked != "" {
		t.Fatal("the budget must not be read for either")
	}
}

func TestAFailedReadIsAServerErrorWithoutDetail(t *testing.T) {
	source := &fakeBudgetSource{err: errors.New("pq: connection refused at 10.0.0.1")}
	w := getBudget(t, budgetRouter(NewPlanBudgetHandler(source, onlyMember{"o1", "u1"})), "o1", "u1")
	if w.Code != http.StatusInternalServerError || w.Body.String() == "" || strings.Contains(w.Body.String(), "10.0.0.1") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
