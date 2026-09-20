package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A project whose restore is still in flight carries working-looking
// credentials for a database nothing has confirmed. Until the restore says
// otherwise it is not a project the platform serves — and it has to be
// deletable, because the process driving the restore may be gone.
func TestRestoringProjectRefusesEveryRouteButStatusAndDelete(t *testing.T) {
	r, memberID := deletingProjectRouter(t, string(domain.StatusRestoring))
	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/provision/" + testProject + "/", http.StatusOK},
		{http.MethodDelete, "/api/provision/" + testProject + "/", http.StatusOK},
		{http.MethodPost, "/api/provision/" + testProject + "/credentials/rotate", http.StatusConflict},
		{http.MethodPost, "/api/provision/" + testProject + "/pause", http.StatusConflict},
		{http.MethodPost, "/api/provision/" + testProject + "/resume", http.StatusConflict},
		{http.MethodPost, "/api/provision/" + testProject + "/backup/trigger", http.StatusConflict},
		{http.MethodPost, "/api/provision/" + testProject + "/backup/restore", http.StatusConflict},
		{http.MethodPost, "/api/provision/" + testProject + "/rls-policies", http.StatusConflict},
		{http.MethodPost, "/api/provision/" + testProject + "/table-grants", http.StatusConflict},
		{http.MethodPost, "/api/projects/" + testProject + "/functions", http.StatusConflict},
		{http.MethodPut, "/api/projects/" + testProject + "/cors", http.StatusConflict},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req = req.WithContext(projectRequest(tc.method, testProject, memberUser(memberID), nil).Context())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

func TestRestoringProjectSaysWhatIsHappening(t *testing.T) {
	r, memberID := deletingProjectRouter(t, string(domain.StatusRestoring))
	req := httptest.NewRequest(http.MethodPost, "/api/provision/"+testProject+"/pause", nil)
	req = req.WithContext(projectRequest(http.MethodPost, testProject, memberUser(memberID), nil).Context())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), "being restored") {
		t.Errorf("body must name the restore, got %s", w.Body.String())
	}
}
