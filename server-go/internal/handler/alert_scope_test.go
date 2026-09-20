package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// A platform-wide alert: it names no project, so it belongs to whoever
	// reads the platform rather than to a tenant. Added last so the history
	// limit would cut the caller's own alert first if scoping ran too late.
	svc.AddAlert(domain.Alert{ID: "p", Severity: "WARNING", Message: "platform"})

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
	if len(alerts) != 4 {
		t.Fatalf("PermViewAny saw %v, want every alert the platform holds", alertProjectIDs(alerts))
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

// EXC-418: an alert that names no project is a platform-wide one. Refusing
// every project-less alert made those invisible to everybody, which is the
// opposite of what an alert is for.
func TestPlatformWideAlertsReachThePlatformReaders(t *testing.T) {
	h := alertScopeHandler(t)

	_, alerts := alertRequest(t, h.GetActive, operatorUser(), sessionToken())
	platformWide := 0
	for _, a := range alerts {
		if a.ProjectID == "" {
			platformWide++
		}
	}
	if platformWide != 1 {
		t.Fatalf("a platform operator saw %d platform-wide alerts, want 1", platformWide)
	}
}

func TestPlatformWideAlertsStayHiddenFromTenants(t *testing.T) {
	h := alertScopeHandler(t)
	member := &domain.User{ID: alertMemberA, Role: "user", Active: true}

	_, alerts := alertRequest(t, h.GetActive, member, &domain.AccessToken{})
	for _, a := range alerts {
		if a.ProjectID == "" {
			t.Fatal("a tenant saw a platform-wide alert")
		}
	}
}

// The limit used to cut the history before the scoping did, so a tenant whose
// alerts were older than the busiest tenant's got an empty page.
func TestAlertHistoryLimitsAfterScopingNotBefore(t *testing.T) {
	h := alertScopeHandler(t)
	member := &domain.User{ID: alertMemberA, Role: "user", Active: true}

	req := httptest.NewRequest(http.MethodGet, "/api/alerts/history?limit=1", nil)
	ctx := auth.SetUser(req.Context(), member)
	ctx = auth.SetToken(ctx, &domain.AccessToken{})
	w := httptest.NewRecorder()
	h.GetHistory(w, req.WithContext(ctx))

	var alerts []domain.Alert
	if err := json.Unmarshal(w.Body.Bytes(), &alerts); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(alerts) != 1 || alerts[0].ProjectID != alertProjectA {
		t.Fatalf("got %v, want the caller's own single alert", alertProjectIDs(alerts))
	}
}

func TestMostRecentKeepsTheTailOfTheHistory(t *testing.T) {
	history := []domain.Alert{{ID: "1"}, {ID: "2"}, {ID: "3"}}
	cases := map[int][]string{
		0:  {"1", "2", "3"},
		-1: {"1", "2", "3"},
		9:  {"1", "2", "3"},
		2:  {"2", "3"},
	}
	for limit, want := range cases {
		got := []string{}
		for _, a := range mostRecent(history, limit) {
			got = append(got, a.ID)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("limit %d: got %v, want %v", limit, got, want)
		}
	}
}
