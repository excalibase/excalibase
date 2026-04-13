package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
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
