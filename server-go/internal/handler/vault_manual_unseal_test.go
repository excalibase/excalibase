package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
)

type vaultAuditRecorder struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (a *vaultAuditRecorder) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, *entry)
	return nil
}

func (a *vaultAuditRecorder) actions() []domain.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domain.AuditEntry(nil), a.entries...)
}

// manualVaultRouter mounts the vault routes the way main.go does for manual
// unseal (EXC-579): audited, rate limited, the provider reported.
func manualVaultRouter(t *testing.T, limit int) (chi.Router, *vault.Vault, *vaultAuditRecorder) {
	t.Helper()
	v, err := vault.NewWithStore(vault.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	audit := &vaultAuditRecorder{}
	h := NewVaultHandler(v)
	h.SetAuditLog(audit)
	h.SetLifecycleRateLimit(custommw.RateLimit(custommw.PerIP, limit, time.Minute))
	h.SetUnsealProvider("manual")
	r := chi.NewRouter()
	r.Use(fakeAuthMiddleware)
	r.Route("/api/vault", h.Routes)
	return r, v, audit
}

func postJSON(r http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.7:4000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestVaultUnseal_IsAuditedWithoutTheShare(t *testing.T) {
	r, v, audit := manualVaultRouter(t, 100)
	result, err := v.Init(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	v.Seal()

	if w := postJSON(r, testVaultUnsealPath, `{"share":"not-a-share"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad share: %d", w.Code)
	}
	if w := postJSON(r, testVaultUnsealPath, `{"share":"`+result.Shares[0]+`"}`); w.Code != http.StatusOK {
		t.Fatalf("unseal: %d %s", w.Code, w.Body)
	}

	entries := audit.actions()
	if len(entries) != 2 {
		t.Fatalf("audit entries = %+v, want a refused and an unsealed one", entries)
	}
	for i, want := range []string{`"outcome":"refused"`, `"outcome":"unsealed"`} {
		entry := entries[i]
		if entry.Action != "vault.unseal" || entry.UserID != "test-admin" || entry.IPAddress == "" {
			t.Errorf("entry %d = %+v", i, entry)
		}
		if !strings.Contains(entry.Details, want) {
			t.Errorf("entry %d details %s, want %s", i, entry.Details, want)
		}
		if strings.Contains(entry.Details, result.Shares[0]) || strings.Contains(entry.Details, "not-a-share") {
			t.Errorf("entry %d carries the submitted share: %s", i, entry.Details)
		}
	}
}

func TestVaultInitAndSeal_AreAudited(t *testing.T) {
	r, _, audit := manualVaultRouter(t, 100)
	w := postJSON(r, testVaultInitPath, `{"shares":3,"threshold":2}`)
	if w.Code != http.StatusOK {
		t.Fatalf("init: %d", w.Code)
	}
	var body struct{ Shares []string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w := postJSON(r, "/api/vault/seal", ``); w.Code != http.StatusOK {
		t.Fatalf("seal: %d", w.Code)
	}
	entries := audit.actions()
	if len(entries) != 2 || entries[0].Action != "vault.init" || entries[1].Action != "vault.seal" {
		t.Fatalf("audit = %+v", entries)
	}
	for _, share := range body.Shares {
		if strings.Contains(entries[0].Details, share) {
			t.Fatal("the init audit entry carries a share")
		}
	}
}

func TestVaultUnseal_IsRateLimited(t *testing.T) {
	r, v, _ := manualVaultRouter(t, 3)
	if _, err := v.Init(1, 1); err != nil {
		t.Fatal(err)
	}
	v.Seal()
	for i := 0; i < 3; i++ {
		if w := postJSON(r, testVaultUnsealPath, `{"share":"guess"}`); w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := postJSON(r, testVaultUnsealPath, `{"share":"guess"}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("4th attempt: %d, want 429", w.Code)
	}
}

func TestVaultStatus_ReportsTheUnsealProvider(t *testing.T) {
	r, _, _ := manualVaultRouter(t, 100)
	req := httptest.NewRequest(http.MethodGet, "/api/vault/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var status map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["unsealProvider"] != "manual" {
		t.Fatalf("status = %v, want unsealProvider manual", status)
	}
}
