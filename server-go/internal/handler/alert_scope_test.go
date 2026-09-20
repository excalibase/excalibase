package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// EXC-418: /api/alerts and /api/alerts/history named no tenant and returned
// every project's alerts to any authenticated caller — a project inventory
// and its failure modes handed to anyone with a login. The row says the
// results are scoped; these tests say the handler actually scopes them.

const (
	alertOrgA     = "org-a"
	alertOrgB     = "org-b"
	alertProjectA = "proj-alert-a"
	alertProjectB = "proj-alert-b"
	alertMemberA  = "member-alert-a"
)

func alertScopeHandler(t *testing.T) *AlertHandler {
	t.Helper()
	svc := service.NewAlertingService(t.TempDir())
	svc.AddAlert(domain.Alert{ID: "a", ProjectID: alertProjectA, Severity: "WARNING", Message: "a"})
	svc.AddAlert(domain.Alert{ID: "b", ProjectID: alertProjectB, Severity: "WARNING", Message: "b"})
	// A second alert on the same project: the visibility of a project is
	// resolved once and reused, so the list must not drop the repeat.
	svc.AddAlert(domain.Alert{ID: "b2", ProjectID: alertProjectB, Severity: "WARNING", Message: "b2"})

	instances := fakestore.NewInstances()
	instances.Items[alertProjectA] = &domain.DatabaseInstance{ProjectID: alertProjectA, OrgID: alertOrgA, Status: "ACTIVE"}
	instances.Items[alertProjectB] = &domain.DatabaseInstance{ProjectID: alertProjectB, OrgID: alertOrgB, Status: "ACTIVE"}
	orgs := fakestore.NewOrgs()
	orgs.AddMember(alertOrgA, alertMemberA, domain.OrgRoleViewer)

	h := NewAlertHandler(svc)
	h.SetScope(instances, orgs)
	return h
}

// alertRequest drives one alert read as the given user and token.
func alertRequest(t *testing.T, serve http.HandlerFunc, user *domain.User, token *domain.AccessToken) (int, []domain.Alert) {
	t.Helper()
	ctx := context.Background()
	if user != nil {
		ctx = auth.SetUser(ctx, user)
	}
	if token != nil {
		ctx = auth.SetToken(ctx, token)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/alerts/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	serve(w, req)
	var alerts []domain.Alert
	_ = json.Unmarshal(w.Body.Bytes(), &alerts)
	return w.Code, alerts
}

func alertProjectIDs(alerts []domain.Alert) []string {
	out := make([]string, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, a.ProjectID)
	}
	return out
}

func TestActiveAlertsShowOnlyTheCallersProjects(t *testing.T) {
	h := alertScopeHandler(t)
	user := &domain.User{ID: alertMemberA, Role: "user", Active: true}

	code, alerts := alertRequest(t, h.GetActive, user, &domain.AccessToken{})
	if code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	if got := alertProjectIDs(alerts); len(got) != 1 || got[0] != alertProjectA {
		t.Fatalf("a member of org A saw %v", got)
	}
}

func TestAlertHistoryShowsOnlyTheCallersProjects(t *testing.T) {
	h := alertScopeHandler(t)
	user := &domain.User{ID: alertMemberA, Role: "user", Active: true}

	code, alerts := alertRequest(t, h.GetHistory, user, &domain.AccessToken{})
	if code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	if got := alertProjectIDs(alerts); len(got) != 1 || got[0] != alertProjectA {
		t.Fatalf("a member of org A saw %v", got)
	}
}

func TestPlatformOperatorSeesEveryProjectsAlerts(t *testing.T) {
	h := alertScopeHandler(t)
	user := &domain.User{ID: "op", Role: "platform_operator", Active: true}

	_, alerts := alertRequest(t, h.GetActive, user, &domain.AccessToken{})
	if len(alerts) != 3 {
		t.Fatalf("PermViewAny saw %v, want every project's alerts", alertProjectIDs(alerts))
	}
}

// A platform operator holding a project-bound PAT is still bound by it: the
// credential is narrowed even though the role is not.
func TestABoundTokenNeverWidensToOtherProjectsAlerts(t *testing.T) {
	h := alertScopeHandler(t)
	user := &domain.User{ID: "op", Role: "platform_operator", Active: true}

	_, alerts := alertRequest(t, h.GetActive, user, &domain.AccessToken{ProjectID: alertProjectB})
	got := alertProjectIDs(alerts)
	if len(got) != 2 || got[0] != alertProjectB || got[1] != alertProjectB {
		t.Fatalf("a token bound to project B saw %v", got)
	}
}

func TestAlertsRefuseAnUnauthenticatedCaller(t *testing.T) {
	h := alertScopeHandler(t)
	code, _ := alertRequest(t, h.GetActive, nil, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

// Without the stores there is no way to scope, and the unfiltered list is
// exactly what must not be returned.
func TestAlertsRefuseWhenScopingIsNotWired(t *testing.T) {
	h := NewAlertHandler(service.NewAlertingService(t.TempDir()))
	code, _ := alertRequest(t, h.GetActive, &domain.User{ID: "u", Role: "user"}, &domain.AccessToken{})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", code)
	}
}
