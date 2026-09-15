package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/byoc"
)

func TestBYOCHandler_MissingFields(t *testing.T) {
	h := &ProvisioningHandler{}

	tests := []struct {
		name string
		body string
	}{
		{"missing host", `{"projectName":"a","orgId":"o","port":5432,"database":"d","username":"u","password":"p"}`},
		{"missing projectName", `{"orgId":"o","host":"h","port":5432,"database":"d","username":"u","password":"p"}`},
		{"missing password", `{"projectName":"a","orgId":"o","host":"h","port":5432,"database":"d","username":"u"}`},
		{"zero port", `{"projectName":"a","orgId":"o","host":"h","port":0,"database":"d","username":"u","password":"p"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/byoc", bytes.NewBufferString(tt.body))
			w := httptest.NewRecorder()
			h.ProvisionBYOC(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestBYOCHandler_InvalidJSON(t *testing.T) {
	h := &ProvisioningHandler{}

	req := httptest.NewRequest("POST", "/byoc", bytes.NewBufferString("not json"))
	w := httptest.NewRecorder()
	h.ProvisionBYOC(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func postBYOC(h *ProvisioningHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/byoc", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ProvisionBYOC(w, req)
	return w
}

func byocBody(host, username, database, password string) string {
	return `{"projectName":"a","orgId":"o","host":"` + host + `","port":5432,"database":"` + database +
		`","username":"` + username + `","password":"` + password + `"}`
}

func TestBYOCHandler_RejectsSSRFTargets(t *testing.T) {
	h := newBYOCTestHandler(byoc.Policy{}, tableResolver{
		"internal.example": {"10.42.7.9"},
		"public.example":   {"203.0.113.10"},
	})
	cases := []struct {
		name string
		body string
	}{
		{"dns answer is private", byocBody("internal.example", "u", "d", "p")},
		{"ip literal is metadata", byocBody("169.254.169.254", "u", "d", "p")},
		{"ipv6 loopback", byocBody("::1", "u", "d", "p")},
		{"v4-mapped metadata", byocBody("::ffff:169.254.169.254", "u", "d", "p")},
		{"multi-host dsn", byocBody("public.example,10.0.0.1", "u", "d", "p")},
		{"hostaddr injected via host", byocBody("public.example hostaddr=10.0.0.1", "u", "d", "p")},
		{"host injected via username", byocBody("public.example", "u host=127.0.0.1", "d", "p")},
		{"host injected via database", byocBody("public.example", "u", "d host=127.0.0.1", "p")},
		{"host injected via password", byocBody("public.example", "u", "d", "p host=127.0.0.1")},
		{"unresolvable", byocBody("nxdomain.example", "u", "d", "p")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postBYOC(h, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestBYOCHandler_ErrorDoesNotLeakResolvedAddress(t *testing.T) {
	h := newBYOCTestHandler(byoc.Policy{}, tableResolver{"internal.example": {"10.42.7.9"}})
	w := postBYOC(h, byocBody("internal.example", "u", "d", "p"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "10.42.7.9") {
		t.Errorf("response leaks the resolved internal address: %s", w.Body.String())
	}
}

func TestBYOCHandler_EgressAllowlist(t *testing.T) {
	policy, err := byoc.ParseAllowlist("*.rds.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	h := newBYOCTestHandler(policy, tableResolver{
		"prod.rds.amazonaws.com": {"203.0.113.10"},
		"other.example":          {"203.0.113.11"},
	})
	if w := postBYOC(h, byocBody("other.example", "u", "d", "p")); w.Code != http.StatusBadRequest {
		t.Errorf("unlisted host: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	// Listed host passes validation; the nil service then panics inside the
	// handler, so we only assert the validation gate by recovering.
	defer func() {
		if recover() == nil {
			t.Error("listed host should have passed validation and reached the service call")
		}
	}()
	postBYOC(h, byocBody("prod.rds.amazonaws.com", "u", "d", "p"))
}
