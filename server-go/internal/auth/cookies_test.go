package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const plantedName = "excali_session"

// ownersLookup maps raw tokens to the user that owns them.
type ownersLookup struct{ byRaw map[string]string }

func (o ownersLookup) FindByTokenHash(_ context.Context, hash string) (*domain.AccessToken, error) {
	for raw, owner := range o.byRaw {
		if HashToken(raw) == hash {
			return &domain.AccessToken{TokenHash: hash, UserID: owner}, nil
		}
	}
	return nil, nil
}

func (o ownersLookup) FindUserByID(_ context.Context, id string) (*domain.User, error) {
	return &domain.User{ID: id, Active: true, Role: "user"}, nil
}

// signedInAs runs a request through ExtractAuth and reports who it became.
func signedInAs(t *testing.T, cookies ...*http.Cookie) string {
	t.Helper()
	lookup := ownersLookup{byRaw: map[string]string{"victim-token": "victim", "planted-token": "attacker"}}
	var got string
	h := ExtractAuth(lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := GetUser(r.Context()); user != nil {
			got = user.ID
		}
	}))
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestSessionCookie_PlantedUnprefixedCookieNeverWins(t *testing.T) {
	planted := &http.Cookie{Name: plantedName, Value: "planted-token"}
	hostSession := &http.Cookie{Name: SessionCookieName, Value: "victim-token"}
	if got := signedInAs(t, planted, hostSession); got != "victim" {
		t.Fatalf("signed in as %q, want the __Host- session's owner", got)
	}
	if got := signedInAs(t, hostSession, planted); got != "victim" {
		t.Fatalf("order must not matter, signed in as %q", got)
	}
}

func TestSessionCookie_OnlyAPlantedCookieAuthenticatesNobody(t *testing.T) {
	for _, name := range []string{plantedName, "Excali_Session", " excali_session", "_Host-excali_session"} {
		if got := signedInAs(t, &http.Cookie{Name: name, Value: "planted-token"}); got != "" {
			t.Errorf("cookie %q signed in as %q", name, got)
		}
	}
}

func TestSessionCookie_DuplicatesAreRefused(t *testing.T) {
	first := &http.Cookie{Name: SessionCookieName, Value: "victim-token"}
	second := &http.Cookie{Name: SessionCookieName, Value: "planted-token"}
	if got := signedInAs(t, first, second); got != "" {
		t.Fatalf("two session cookies signed in as %q", got)
	}
	if got := signedInAs(t, second, first); got != "" {
		t.Fatalf("two session cookies signed in as %q", got)
	}
}

func TestSessionCookie_InsecureOptInReadsTheUnprefixedName(t *testing.T) {
	UseInsecureCookies(true)
	t.Cleanup(func() { UseInsecureCookies(false) })
	if got := signedInAs(t, &http.Cookie{Name: plantedName, Value: "victim-token"}); got != "victim" {
		t.Fatalf("http installs sign in with the plain name, got %q", got)
	}
	if got := signedInAs(t, &http.Cookie{Name: SessionCookieName, Value: "victim-token"}); got != "" {
		t.Fatalf("the prefixed name must not be read in insecure mode, got %q", got)
	}
}

func TestCookieName_DefaultIsPrefixedAndSecure(t *testing.T) {
	if got := CookieName("excali_x"); got != "__Host-excali_x" {
		t.Fatalf("name = %q", got)
	}
	if !SecureCookies() {
		t.Fatal("cookies must be Secure by default")
	}
	UseInsecureCookies(true)
	t.Cleanup(func() { UseInsecureCookies(false) })
	if got := CookieName("excali_x"); got != "excali_x" || SecureCookies() {
		t.Fatalf("insecure mode: name %q secure %v", got, SecureCookies())
	}
}

func TestReadCookie_RefusesMissingAndDuplicate(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if _, ok := ReadCookie(req, "excali_x"); ok {
		t.Fatal("a missing cookie read as present")
	}
	req.AddCookie(&http.Cookie{Name: "__Host-excali_x", Value: "a"})
	if v, ok := ReadCookie(req, "excali_x"); !ok || v != "a" {
		t.Fatalf("single cookie: %q %v", v, ok)
	}
	req.AddCookie(&http.Cookie{Name: "__Host-excali_x", Value: "b"})
	if _, ok := ReadCookie(req, "excali_x"); ok {
		t.Fatal("duplicates read as present")
	}
}

func TestClearLegacyCookie_ExpiresTheUnprefixedName(t *testing.T) {
	rec := httptest.NewRecorder()
	ClearLegacyCookie(rec, "excali_x")
	got := rec.Header().Get("Set-Cookie")
	for _, want := range []string{"excali_x=;", "Path=/", "Max-Age=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "__Host-") {
		t.Errorf("clears the wrong name: %q", got)
	}
}
