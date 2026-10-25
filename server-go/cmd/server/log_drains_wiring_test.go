package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
)

// EXC-658: the forwarder's routes answer only on the internal listener, so
// the edge cannot reach them whatever token it is handed.
func TestLogForwarderRoutesAnswerOnTheInternalListenerOnly(t *testing.T) {
	router, _ := darkFeatureRouter(t, features.LogDrains)
	public, internal := newListeners(config.AppConfig{}, router)
	pull := func(handler http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, logdrain.PullPath, strings.NewReader(`{"max":1}`))
		req.Header.Set(logdrain.TokenHeader, "matrix-forwarder-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := pull(public.Handler); rec.Code != http.StatusNotFound {
		t.Errorf("public listener: %d %s", rec.Code, rec.Body)
	}
	if rec := pull(internal.Handler); rec.Code != http.StatusOK {
		t.Errorf("internal listener: %d %s", rec.Code, rec.Body)
	}
}
