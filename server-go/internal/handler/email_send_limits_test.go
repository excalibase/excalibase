package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// countingUsers counts address lookups and can hold them until released.
type countingUsers struct {
	mu      sync.Mutex
	lookups map[string]int
	hold    chan struct{}
}

func (c *countingUsers) FindUserByEmail(_ context.Context, address string) (*domain.User, error) {
	if c.hold != nil {
		<-c.hold
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookups[address]++
	return nil, nil
}

func (c *countingUsers) FindUserByID(context.Context, string) (*domain.User, error) { return nil, nil }
func (c *countingUsers) UpdateUserPassword(context.Context, string, string) error   { return nil }

func (c *countingUsers) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, count := range c.lookups {
		n += count
	}
	return n
}

func mailRouter(users emailTokenUsers, sync bool) (chi.Router, *EmailTokensHandler) {
	h := NewEmailTokensHandler(nil, &recordingSender{}, users, testStudioURL, "")
	h.SetVerifier(NewEmailVerifier(newFakeVerificationStore(), &recordingSender{}, testStudioURL, ""))
	if sync {
		h.runInBackground = func(f func()) { f() }
	}
	r := chi.NewRouter()
	h.Routes(r)
	return r, h
}

func postFromIP(r http.Handler, path, ip, body string) int {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestResetSendIsLimitedPerClient(t *testing.T) {
	r, _ := mailRouter(&countingUsers{lookups: map[string]int{}}, true)
	last := 0
	for i := 0; i < 10; i++ {
		last = postFromIP(r, "/reset/send", "203.0.113.5", fmt.Sprintf(`{"email":"user%d@example.com"}`, i))
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("ten reset mails from one client: last answered %d, want 429", last)
	}
}

// Rotating clients cannot mail-bomb one mailbox: past the per-address budget
// the answer is unchanged and nothing more is looked up or sent.
func TestResetSendIsCappedPerAddressWithoutChangingTheAnswer(t *testing.T) {
	users := &countingUsers{lookups: map[string]int{}}
	r, _ := mailRouter(users, true)
	for i := 0; i < 8; i++ {
		ip := fmt.Sprintf("198.51.100.%d", i)
		if code := postFromIP(r, "/reset/send", ip, `{"email":"Victim@Example.com "}`); code != http.StatusOK {
			t.Fatalf("send %d answered %d; the answer must not reveal the cap", i+1, code)
		}
	}
	if got := users.total(); got != 3 {
		t.Fatalf("mails attempted for one address: got %d, want 3", got)
	}
}

func TestResendAndResetShareTheAddressBudget(t *testing.T) {
	users := &countingUsers{lookups: map[string]int{}}
	r, _ := mailRouter(users, true)
	for i := 0; i < 3; i++ {
		postFromIP(r, "/verify/resend", fmt.Sprintf("198.51.100.%d", i), `{"email":"dev@example.com"}`)
	}
	postFromIP(r, "/reset/send", "198.51.100.9", `{"email":"dev@example.com"}`)
	if got := users.total(); got != 3 {
		t.Fatalf("lookups across both mail routes: got %d, want 3", got)
	}
}

// The answer is written before the address is even looked up, so how long a
// registered address takes (insert, provider round trip) never shows.
func TestResetSendAnswersBeforeLookingTheAddressUp(t *testing.T) {
	users := &countingUsers{lookups: map[string]int{}, hold: make(chan struct{})}
	defer close(users.hold)
	r, _ := mailRouter(users, false)

	done := make(chan int, 1)
	go func() { done <- postFromIP(r, "/reset/send", "203.0.113.7", `{"email":"dev@example.com"}`) }()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("got %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the response waited on the address lookup")
	}
}

func TestResendAnswersBeforeLookingTheAddressUp(t *testing.T) {
	users := &countingUsers{lookups: map[string]int{}, hold: make(chan struct{})}
	defer close(users.hold)
	r, _ := mailRouter(users, false)

	done := make(chan int, 1)
	go func() { done <- postFromIP(r, "/verify/resend", "203.0.113.7", `{"email":"dev@example.com"}`) }()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("got %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the response waited on the address lookup")
	}
}

func TestConfirmRoutesAreLimitedPerClient(t *testing.T) {
	for _, path := range []string{"/reset/confirm", "/verify/confirm"} {
		r, _ := mailRouter(&countingUsers{lookups: map[string]int{}}, true)
		last := 0
		for i := 0; i < 40; i++ {
			last = postFromIP(r, path, "203.0.113.8", `{}`)
		}
		if last != http.StatusTooManyRequests {
			t.Errorf("%s: forty attempts from one client, last answered %d, want 429", path, last)
		}
	}
}

// Studio tells the caller how long the link lives, from the same lifetime the
// link is stored with, and every address gets that same answer.
func TestResetSendAnswersWithTheLinkLifetime(t *testing.T) {
	r, _ := mailRouter(&countingUsers{lookups: map[string]int{}}, true)
	for _, address := range []string{"dev@example.com", "nobody@example.com"} {
		req := httptest.NewRequest(http.MethodPost, "/reset/send", strings.NewReader(`{"email":"`+address+`"}`))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var answer struct {
			Status           string `json:"status"`
			ExpiresInMinutes int    `json:"expiresInMinutes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
			t.Fatalf("%s: decode %q: %v", address, w.Body.String(), err)
		}
		if answer.Status != "sent" || answer.ExpiresInMinutes != int(passwordResetLifetime.Minutes()) {
			t.Fatalf("%s: answer = %+v", address, answer)
		}
	}
}
