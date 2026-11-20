package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestRequireUnboundCredential(t *testing.T) {
	cases := map[string]struct {
		token *domain.AccessToken
		want  int
	}{
		"no token":               {nil, http.StatusOK},
		"session":                {&domain.AccessToken{Scopes: ScopeSession}, http.StatusOK},
		"unbound read and write": {&domain.AccessToken{Scopes: ScopeRead + "," + ScopeWrite}, http.StatusOK},
		"bound to one project":   {&domain.AccessToken{ProjectID: "proj-a", Scopes: ScopeRead + "," + ScopeWrite}, http.StatusForbidden},
		"bound with no scopes":   {&domain.AccessToken{ProjectID: "proj-a"}, http.StatusForbidden},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			handler := RequireUnboundCredential(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.token != nil {
				req = req.WithContext(SetToken(req.Context(), tc.token))
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
