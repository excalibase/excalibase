package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestExtractUserID(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := extractUserID(r); got != "" {
		t.Errorf("no user → empty, got %q", got)
	}
	r = r.WithContext(auth.SetUser(r.Context(), &domain.User{ID: "u-7"}))
	if got := extractUserID(r); got != "u-7" {
		t.Errorf("extractUserID: got %q, want u-7", got)
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name string
		xff  string
		addr string
		want string
	}{
		{"forwarded single", "203.0.113.9", "10.0.0.1:5", "203.0.113.9"},
		{"forwarded chain", "203.0.113.9, 10.0.0.1", "10.0.0.1:5", "203.0.113.9"},
		{"no forwarded", "", "192.0.2.1:9999", "192.0.2.1:9999"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.addr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := clientIP(r); got != c.want {
				t.Errorf("clientIP: got %q, want %q", got, c.want)
			}
		})
	}
}

func TestMintToken_ProducesMatchingHash(t *testing.T) {
	token, hash, err := mintToken()
	if err != nil {
		t.Fatalf("mintToken: %v", err)
	}
	if len(token) != 64 { // 32 random bytes hex-encoded
		t.Errorf("token length: got %d, want 64", len(token))
	}
	if hashToken(token) != hash {
		t.Errorf("hashToken(token) != mint hash")
	}
}

func TestMintToken_Unique(t *testing.T) {
	t1, _, _ := mintToken()
	t2, _, _ := mintToken()
	if t1 == t2 {
		t.Error("two mints produced identical tokens")
	}
}

func TestHashToken_Stable(t *testing.T) {
	a, b := hashToken("abc"), hashToken("abc")
	if a != b {
		t.Error("hashToken not deterministic")
	}
	if hashToken("abc") == hashToken("abd") {
		t.Error("hashToken collision on distinct inputs")
	}
}

func TestLogEmailSent_DoesNotPanic(t *testing.T) {
	// Pure logging side effect; assert it runs without panicking.
	logEmailSent("verify", "u@example.com", "msg-1", "user-1")
}
