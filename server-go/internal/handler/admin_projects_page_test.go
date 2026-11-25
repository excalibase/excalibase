package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"context"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

type projectsPage struct {
	Items  []map[string]interface{} `json:"items"`
	Total  int                      `json:"total"`
	Limit  int                      `json:"limit"`
	Offset int                      `json:"offset"`
}

// pagedAdmin serves n projects named p-000.., owned alternately by an active
// and a suspended owner, on alternating plans, created one hour apart.
func pagedAdmin(n int) *AdminHandler {
	insts := map[string]*domain.DatabaseInstance{}
	facts := map[string]adminconsole.ProjectFact{}
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("p-%03d", i)
		created := domain.FlexTime{Time: base.Add(time.Duration(i) * time.Hour)}
		tier, owner, status := domain.TierType("FREE"), "active", "ACTIVE"
		if i%2 == 1 {
			tier, owner = "STANDARD", "suspended"
		}
		if i%5 == 0 {
			status = "FAILED"
		}
		protected := i%7 == 0
		insts[id] = &domain.DatabaseInstance{
			ProjectID: id, ProjectName: fmt.Sprintf("Project %03d", i), OrgID: "org-1", Tier: tier,
			Status: status, Namespace: "ns-" + id, CreatedAt: &created, DeletionProtection: &protected,
		}
		facts[id] = adminconsole.ProjectFact{OrgName: "Acme", OwnerID: "owner-" + owner, OwnerEmail: fmt.Sprintf("dev%03d@acme.test", i), OwnerStatus: owner}
	}
	h := NewAdminHandler(nil, &inMemoryInstanceStore{insts: insts}, nil, nil, nil, "", nil)
	h.SetProjectFacts(func(context.Context) (map[string]adminconsole.ProjectFact, error) { return facts, nil })
	return h
}

func getProjects(t *testing.T, h *AdminHandler, query string) (*httptest.ResponseRecorder, projectsPage) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ListAllProjects(w, httptest.NewRequest("GET", "/api/admin/projects"+query, nil))
	var page projectsPage
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	return w, page
}

func ids(page projectsPage) []string {
	out := []string{}
	for _, item := range page.Items {
		out = append(out, item["projectId"].(string))
	}
	return out
}

func TestListAllProjects_WithoutLimitKeepsTheBareArray(t *testing.T) {
	w := httptest.NewRecorder()
	pagedAdmin(3).ListAllProjects(w, httptest.NewRequest("GET", "/api/admin/projects", nil))
	var legacy []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil || len(legacy) != 3 {
		t.Fatalf("want the bare array of 3 projects, got %s (%v)", w.Body.String(), err)
	}
}

func TestListAllProjects_PagesAndReportsTheTotal(t *testing.T) {
	h := pagedAdmin(30)
	_, first := getProjects(t, h, "?limit=10&offset=0&sort=name&order=asc")
	_, third := getProjects(t, h, "?limit=10&offset=20&sort=name&order=asc")
	if first.Total != 30 || len(first.Items) != 10 || ids(first)[0] != "p-000" {
		t.Fatalf("first page: total=%d ids=%v", first.Total, ids(first))
	}
	if len(third.Items) != 10 || ids(third)[0] != "p-020" {
		t.Fatalf("third page: %v", ids(third))
	}
	if first.Limit != 10 || third.Offset != 20 {
		t.Fatalf("limit/offset not echoed: %+v %+v", first, third)
	}
}

func TestListAllProjects_OffsetPastTheEndIsAnEmptyPageWithTheTotal(t *testing.T) {
	_, page := getProjects(t, pagedAdmin(5), "?limit=10&offset=50")
	if len(page.Items) != 0 || page.Total != 5 {
		t.Fatalf("want empty page and total 5, got %+v", page)
	}
}

func TestListAllProjects_LimitIsCapped(t *testing.T) {
	_, page := getProjects(t, pagedAdmin(250), "?limit=100000")
	if len(page.Items) != adminconsole.MaxPageSize || page.Limit != adminconsole.MaxPageSize || page.Total != 250 {
		t.Fatalf("limit not capped: items=%d limit=%d total=%d", len(page.Items), page.Limit, page.Total)
	}
}

func TestListAllProjects_SortsByAnAllowedColumnBothWays(t *testing.T) {
	h := pagedAdmin(12)
	_, newest := getProjects(t, h, "?limit=3&sort=created&order=desc")
	_, oldest := getProjects(t, h, "?limit=3&sort=created&order=asc")
	if got := ids(newest); got[0] != "p-011" || got[2] != "p-009" {
		t.Fatalf("newest first: %v", got)
	}
	if got := ids(oldest); got[0] != "p-000" {
		t.Fatalf("oldest first: %v", got)
	}
}

func TestListAllProjects_DefaultSortIsNewestFirst(t *testing.T) {
	_, page := getProjects(t, pagedAdmin(5), "?limit=2")
	if got := ids(page); got[0] != "p-004" {
		t.Fatalf("default order: %v", got)
	}
}

func TestListAllProjects_RejectsAnythingOutsideTheSortAllowList(t *testing.T) {
	h := pagedAdmin(3)
	for _, query := range []string{
		"?limit=5&sort=password",
		"?limit=5&sort=name;DROP%20TABLE%20users",
		"?limit=5&sort=name%20desc,(select%201)",
		"?limit=5&sort=name&order=sideways",
	} {
		if w, _ := getProjects(t, h, query); w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", query, w.Code)
		}
	}
}

func TestListAllProjects_RejectsBadPagingNumbers(t *testing.T) {
	h := pagedAdmin(3)
	for _, query := range []string{"?limit=abc", "?limit=-1", "?limit=0", "?limit=5&offset=-2", "?limit=5&offset=x"} {
		if w, _ := getProjects(t, h, query); w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", query, w.Code)
		}
	}
}

func TestListAllProjects_SearchMatchesNameIdOrgAndOwnerEmail(t *testing.T) {
	h := pagedAdmin(30)
	cases := map[string]int{
		"?limit=50&q=project%20007": 1,
		"?limit=50&q=P-01":          10,
		"?limit=50&q=acme":          30,
		"?limit=50&q=dev012@":       1,
		"?limit=50&q=nothing-here":  0,
		"?limit=50&q=%25":           0, // a wildcard is text, not a pattern
	}
	for query, want := range cases {
		if _, page := getProjects(t, h, query); page.Total != want {
			t.Errorf("%s: total %d, want %d", query, page.Total, want)
		}
	}
}

func TestListAllProjects_FiltersByStatusPlanOwnerAndFlag(t *testing.T) {
	h := pagedAdmin(30)
	cases := map[string]int{
		"?limit=50&status=FAILED":                    6,
		"?limit=50&plan=STANDARD":                    15,
		"?limit=50&owner=suspended":                  15,
		"?limit=50&owner=active":                     15,
		"?limit=50&flag=protected":                   5,
		"?limit=50&status=FAILED&plan=FREE":          3,
		"?limit=50&status=FAILED&owner=suspended&q=": 3,
	}
	for query, want := range cases {
		if _, page := getProjects(t, h, query); page.Total != want {
			t.Errorf("%s: total %d, want %d", query, page.Total, want)
		}
	}
}

func TestListAllProjects_RejectsAnUnknownFlagOrOwnerValue(t *testing.T) {
	h := pagedAdmin(3)
	for _, query := range []string{"?limit=5&flag=bogus", "?limit=5&owner=bogus"} {
		if w, _ := getProjects(t, h, query); w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", query, w.Code)
		}
	}
}

func TestListAllProjects_CarriesTheOwnerIDForBulkSuspend(t *testing.T) {
	_, page := getProjects(t, pagedAdmin(2), "?limit=5&sort=name&order=asc")
	if page.Items[0]["ownerId"] != "owner-active" {
		t.Fatalf("ownerId missing: %v", page.Items[0])
	}
}

func TestListAllProjects_UsageIsReadForTheShownPageOnly(t *testing.T) {
	prom := &countingProm{}
	h := pagedAdmin(40)
	h.prom = prom
	getProjects(t, h, "?limit=10")
	if prom.calls != 20 { // a CPU and a memory query per shown project
		t.Fatalf("want 20 Prometheus queries for 10 rows, got %d", prom.calls)
	}
}

type countingProm struct{ calls int }

func (c *countingProm) InstantValue(context.Context, string) (float64, error) {
	c.calls++
	return 1, nil
}

func TestListAllProjects_PagedReadIsStillGatedByThePlatformRole(t *testing.T) {
	h := pagedAdmin(3)
	for role, want := range map[string]int{
		"user": http.StatusForbidden, "": http.StatusForbidden,
		"platform_viewer": http.StatusOK, "platform_operator": http.StatusOK, "platform_admin": http.StatusOK,
	} {
		w := httptest.NewRecorder()
		adminRouterAs(role, h).ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/projects?limit=10&sort=name", nil))
		if w.Code != want {
			t.Errorf("role %q: got %d, want %d", role, w.Code, want)
		}
	}
}

func TestListAllProjects_RejectsAStatusOrPlanOutsideTheAllowList(t *testing.T) {
	h := pagedAdmin(3)
	for _, query := range []string{"?limit=5&status=bogus", "?limit=5&plan=PLATINUM", "?limit=5&status=ACTIVE%27%20OR%201%3D1"} {
		if w, _ := getProjects(t, h, query); w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", query, w.Code)
		}
	}
	if w, page := getProjects(t, h, "?limit=5&plan=standard"); w.Code != http.StatusOK || page.Total != 1 {
		t.Errorf("a plan is matched whatever its case: %d total=%d", w.Code, page.Total)
	}
}

func TestListAllProjects_AMalformedQueryOnTheUnpagedPathIsIgnored(t *testing.T) {
	w := httptest.NewRecorder()
	pagedAdmin(3).ListAllProjects(w, httptest.NewRequest("GET", "/api/admin/projects?x=%zz", nil))
	var legacy []map[string]interface{}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &legacy) != nil || len(legacy) != 3 {
		t.Fatalf("want the bare array, got %d %s", w.Code, w.Body.String())
	}
	if w2, _ := getProjects(t, pagedAdmin(3), "?limit=5&x=%zz"); w2.Code != http.StatusBadRequest {
		t.Fatalf("a malformed paged query is still refused, got %d", w2.Code)
	}
}
