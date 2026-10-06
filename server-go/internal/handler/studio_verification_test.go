package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

type recordingAudit struct{ entries []*domain.AuditEntry }

func (a *recordingAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.entries = append(a.entries, entry)
	return nil
}

type verificationHarness struct {
	router   chi.Router
	users    *mockUserStore
	tokens   *mockTokenStore
	links    *fakeVerificationStore
	sender   *recordingSender
	audit    *recordingAudit
	handler  *AuthHandler
	password string
}

func newVerificationHarness(t *testing.T, sender email.Sender) *verificationHarness {
	t.Helper()
	h := &verificationHarness{
		users: newMockUserStore(), tokens: newMockTokenStore(), links: newFakeVerificationStore(),
		audit: &recordingAudit{}, password: testutil.FixturePassword("Verify1a"),
	}
	if recorder, ok := sender.(*recordingSender); ok {
		h.sender = recorder
	}
	h.handler = NewAuthHandler(h.users, h.tokens)
	h.handler.SetEmailVerifier(NewEmailVerifier(h.links, sender, testStudioURL, ""))
	h.handler.SetAuditLog(h.audit)
	admin := &domain.User{ID: "admin", Role: "platform_admin", Active: true}
	h.users.users[admin.ID] = admin
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.SetUser(r.Context(), admin)))
		})
	})
	r.Post("/register", h.handler.Register)
	r.Post("/login", h.handler.Login)
	r.Post("/users", h.handler.CreateUser)
	r.Post("/users/{userId}/verify-email", h.handler.MarkEmailVerified)
	h.router = r
	return h
}

func (h *verificationHarness) register(username string) (int, map[string]any) {
	body := fmt.Sprintf(`{"username":%q,"email":%q,"password":%q}`, username, username+"@example.com", h.password)
	w := doRequest(h.router, "POST", "/register", body)
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

func (h *verificationHarness) login(username string) (int, map[string]any) {
	w := doRequest(h.router, "POST", "/login", fmt.Sprintf(`{"username":%q,"password":%q}`, username, h.password))
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

func (h *verificationHarness) userNamed(username string) *domain.User {
	for _, user := range h.users.users {
		if user.Username == username {
			return user
		}
	}
	return nil
}

func TestSignUpMailsAVerificationLinkAndIssuesNoSession(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{})

	status, resp := h.register("dana")
	if status != http.StatusCreated {
		t.Fatalf("register: %d %v", status, resp)
	}
	if resp["status"] != "verification_required" || resp["token"] != nil {
		t.Fatalf("register answered %v, want verification_required and no token", resp)
	}
	if len(h.tokens.tokens) != 0 {
		t.Fatal("a session was issued before verification")
	}
	user := h.userNamed("dana")
	if user == nil || user.EmailVerifiedAt != nil {
		t.Fatalf("stored account: %+v", user)
	}
	if len(h.sender.sent) != 1 || h.sender.sent[0].To[0] != "dana@example.com" {
		t.Fatalf("mail sent: %+v", h.sender.sent)
	}
}

func TestAnUnverifiedAccountCannotSignIn(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{})
	h.register("erin")

	status, resp := h.login("erin")
	if status != http.StatusForbidden || resp["code"] != "email_not_verified" {
		t.Fatalf("login: %d %v, want 403 email_not_verified", status, resp)
	}
	if len(h.tokens.tokens) != 0 {
		t.Fatal("a session was issued to an unverified account")
	}
}

func TestAWrongPasswordDoesNotRevealVerificationState(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{})
	h.register("fay")
	w := doRequest(h.router, "POST", "/login", `{"username":"fay","password":"Wrong1password"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}

func TestAVerifiedAccountSignsIn(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{})
	h.register("gus")
	now := time.Now()
	h.userNamed("gus").EmailVerifiedAt = &now

	if status, resp := h.login("gus"); status != http.StatusOK || resp["token"] == nil {
		t.Fatalf("login: %d %v", status, resp)
	}
}

func TestSignUpIsRefusedWhenNoMailCanBeSent(t *testing.T) {
	h := newVerificationHarness(t, email.NewNoopSender())

	if status, _ := h.register("hal"); status != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", status)
	}
	if h.userNamed("hal") != nil {
		t.Fatal("an account was created that could never verify")
	}
}

func TestSignUpWhoseMailFailsLeavesNoAccount(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{err: errors.New("provider down")})

	if status, _ := h.register("ivy"); status != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", status)
	}
	if h.userNamed("ivy") != nil {
		t.Fatal("the account survived a verification mail that was never sent")
	}
}

func TestAdminCreatedAccountStartsUnverifiedAndIsMailed(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{})
	body := fmt.Sprintf(`{"username":"jordan","email":"jo@example.com","password":%q,"role":"user"}`, h.password)
	if w := doRequest(h.router, "POST", "/users", body); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if user := h.userNamed("jordan"); user == nil || user.EmailVerifiedAt != nil {
		t.Fatalf("stored account: %+v", user)
	}
	if len(h.sender.sent) != 1 {
		t.Fatalf("verification mails sent: %d", len(h.sender.sent))
	}
	if status, _ := h.login("jordan"); status != http.StatusForbidden {
		t.Fatalf("login before verification: %d", status)
	}
}

func TestAdminMarksAnAccountVerifiedAndItIsAudited(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{})
	h.register("kim")
	user := h.userNamed("kim")
	h.links.users[user.ID] = user.Email

	w := doRequest(h.router, "POST", "/users/"+user.ID+"/verify-email", "")
	if w.Code != http.StatusOK {
		t.Fatalf("mark verified: %d %s", w.Code, w.Body.String())
	}
	if _, ok := h.links.verified[user.ID]; !ok {
		t.Fatal("the account was not marked verified")
	}
	if len(h.audit.entries) != 1 || h.audit.entries[0].Action != auditActionMarkEmailVerified ||
		h.audit.entries[0].ResourceID != user.ID || h.audit.entries[0].UserID != "admin" {
		t.Fatalf("audit entries: %+v", h.audit.entries)
	}
}

func TestMarkingAnUnknownAccountVerifiedIs404(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{})
	if w := doRequest(h.router, "POST", "/users/nobody/verify-email", ""); w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
	if len(h.audit.entries) != 0 {
		t.Fatal("a refused action was audited")
	}
}

var errNotSent = errors.New("provider down")

func httptestPostAs(handle http.HandlerFunc, user *domain.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/", nil)
	req = req.WithContext(auth.SetUser(req.Context(), user))
	w := httptest.NewRecorder()
	handle(w, req)
	return w
}

func TestMarkEmailVerifiedWithoutAVerifierIs503(t *testing.T) {
	h := NewAuthHandler(newMockUserStore(), newMockTokenStore())
	r := chi.NewRouter()
	r.Post("/users/{userId}/verify-email", h.MarkEmailVerified)
	if w := doRequest(r, "POST", "/users/u1/verify-email", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

type failingVerificationStore struct{ *fakeVerificationStore }

func (failingVerificationStore) MarkEmailVerified(context.Context, string, time.Time) error {
	return errNotSent
}

func TestMarkEmailVerifiedStoreFailureIs500(t *testing.T) {
	h := NewAuthHandler(newMockUserStore(), newMockTokenStore())
	h.SetEmailVerifier(NewEmailVerifier(failingVerificationStore{newFakeVerificationStore()}, &recordingSender{}, testStudioURL, ""))
	r := chi.NewRouter()
	r.Post("/users/{userId}/verify-email", h.MarkEmailVerified)
	if w := doRequest(r, "POST", "/users/u1/verify-email", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
}

// Creating the account does not hang on the mail: an admin can mark it verified.
func TestAdminCreatedAccountSurvivesAFailedMail(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{err: errNotSent})
	body := fmt.Sprintf(`{"username":"lee","email":"lee@example.com","password":%q,"role":"user"}`, h.password)
	if w := doRequest(h.router, "POST", "/users", body); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
}

// A new account's name is 3-32 letters, digits or underscores; the refusal
// says the rule and nothing is created.
func TestRegisterRefusesAUsernameOutsideTheRule(t *testing.T) {
	for _, name := range []string{"jo", strings.Repeat("a", 33), "bad name", "dash-name", "dot.name", "émile", "a;drop"} {
		t.Run(name, func(t *testing.T) {
			h := newVerificationHarness(t, &recordingSender{})
			status, resp := h.register(name)
			if status != http.StatusBadRequest || resp["error"] != usernameRule {
				t.Fatalf("got %d %v", status, resp)
			}
			if h.userNamed(name) != nil {
				t.Fatal("an account was created")
			}
		})
	}
}

func TestRegisterAcceptsAUsernameInsideTheRule(t *testing.T) {
	for _, name := range []string{"bob", "Alice_2", strings.Repeat("a", 32)} {
		h := newVerificationHarness(t, &recordingSender{})
		if status, resp := h.register(name); status != http.StatusCreated {
			t.Fatalf("%s: got %d %v", name, status, resp)
		}
	}
}

func TestAdminCreatedAccountFollowsTheUsernameRule(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{})
	body := fmt.Sprintf(`{"username":"x y","email":"xy@example.com","password":%q,"role":"user"}`, h.password)
	if w := doRequest(h.router, "POST", "/users", body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), usernameRule) {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if h.userNamed("x y") != nil {
		t.Fatal("an account was created")
	}
}

// An account made before the rule existed still signs in.
func TestAnExistingUsernameOutsideTheRuleStillSignsIn(t *testing.T) {
	h := newVerificationHarness(t, &recordingSender{})
	h.register("legacy")
	user := h.userNamed("legacy")
	user.Username = "old.name-x"
	verifiedAt := time.Now()
	user.EmailVerifiedAt = &verifiedAt
	if status, resp := h.login("old.name-x"); status != http.StatusOK {
		t.Fatalf("login: %d %v", status, resp)
	}
}
