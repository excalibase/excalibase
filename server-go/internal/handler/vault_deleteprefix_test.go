package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVaultDeletePrefix(t *testing.T) {
	r, v := setupVaultRouter(t)

	res, err := v.Init(1, 1)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := v.Unseal(res.Shares[0]); err != nil {
		t.Fatalf("unseal: %v", err)
	}
	// Seed two secrets under a common prefix.
	if err := v.Put("projects/org-x/app/a", map[string]string{"k": "1"}); err != nil {
		t.Fatalf("put a: %v", err)
	}
	if err := v.Put("projects/org-x/app/b", map[string]string{"k": "2"}); err != nil {
		t.Fatalf("put b: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/vault/secrets-list?prefix=projects/org-x/app", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DeletePrefix: %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)
	if body["deleted"] == nil {
		t.Errorf("expected deleted count, got %v", body)
	}
}

func TestVaultDeletePrefix_RequiresPrefix(t *testing.T) {
	r, _ := setupVaultRouter(t)
	req := httptest.NewRequest("DELETE", "/api/vault/secrets-list", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing prefix should 400, got %d", w.Code)
	}
}

func TestVaultDeletePrefix_WhenSealed(t *testing.T) {
	r, v := setupVaultRouter(t)
	res, _ := v.Init(1, 1)
	v.Unseal(res.Shares[0])
	_ = v.Put("p/x", map[string]string{"k": "1"})
	v.Seal()

	req := httptest.NewRequest("DELETE", "/api/vault/secrets-list?prefix=p", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("sealed DeletePrefix should 503, got %d", w.Code)
	}
}
