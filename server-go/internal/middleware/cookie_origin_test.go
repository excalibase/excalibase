package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

const studioOrigin = "https://studio.example.test"

type originCase struct {
	name    string
	method  string
	cookie  bool
	bearer  bool
	origin  string
	referer string
	want    int
}

func (c originCase) request() *http.Request {
	r := httptest.NewRequest(c.method, "/api/orgs", nil)
	if c.cookie {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	}
	if c.bearer {
		r.Header.Set("Authorization", "Bearer pat")
	}
	if c.origin != "" {
		r.Header.Set("Origin", c.origin)
	}
	if c.referer != "" {
		r.Header.Set("Referer", c.referer)
	}
	return r
}

func TestRequireTrustedOriginForCookies(t *testing.T) {
	cases := []originCase{
		{"cookie write from Studio", http.MethodPost, true, false, studioOrigin, "", http.StatusOK},
		{"cookie write from another origin", http.MethodPost, true, false, "https://app.example.test", "", http.StatusForbidden},
		{"cookie write from a sibling port", http.MethodDelete, true, false, "https://studio.example.test:8443", "", http.StatusForbidden},
		{"cookie write from an opaque origin", http.MethodPut, true, false, "null", "", http.StatusForbidden},
		{"cookie write with only a Studio referer", http.MethodPatch, true, false, "", studioOrigin + "/orgs/1", http.StatusOK},
		{"cookie write with a foreign referer", http.MethodPost, true, false, "", "https://app.example.test/x", http.StatusForbidden},
		{"cookie write naming no origin at all", http.MethodPost, true, false, "", "", http.StatusForbidden},
		{"cookie read from another origin", http.MethodGet, true, false, "https://app.example.test", "", http.StatusOK},
		{"bearer write from another origin", http.MethodPost, true, true, "https://app.example.test", "", http.StatusOK},
		{"anonymous write from another origin", http.MethodPost, false, false, "https://app.example.test", "", http.StatusOK},
	}
	h := RequireTrustedOriginForCookies([]string{studioOrigin})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, c.request())
			if w.Code != c.want {
				t.Fatalf("got %d, want %d", w.Code, c.want)
			}
		})
	}
}

func TestTrustedOriginsTakeStudioAndExplicitCORSOrigins(t *testing.T) {
	got := TrustedOrigins("https://studio.example.test/", []string{"*", "http://localhost:5173"})
	want := map[string]bool{"https://studio.example.test": true, "http://localhost:5173": true}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for _, o := range got {
		if !want[o] {
			t.Errorf("unexpected trusted origin %q", o)
		}
	}
}

// An unset Studio URL must not turn "no origin" into a trusted one.
func TestAnEmptyTrustedOriginTrustsNothing(t *testing.T) {
	h := RequireTrustedOriginForCookies(TrustedOrigins("", nil))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodPost, "/api/orgs", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", w.Code)
	}
}
