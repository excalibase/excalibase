package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// A lock the pool could not give in time is a retryable 503 on every
// lifecycle surface, not the 500 or 409 their generic arms answer.
func TestLifecycleErrorWriters_PoolBusyIs503WithRetryAfter(t *testing.T) {
	busy := fmt.Errorf("claim project for pause: %w: acquire conn: context deadline exceeded", storage.ErrPoolBusy)
	writers := map[string]func(http.ResponseWriter){
		"deprovision": func(w http.ResponseWriter) { writeDeprovisionError(w, busy) },
		"app network": func(w http.ResponseWriter) { writeAppNetworkError(w, busy) },
		"template":    func(w http.ResponseWriter) { writeTemplateError(w, busy) },
		"major":       func(w http.ResponseWriter) { writeMajorUpgradeError(w, "p1", busy) },
		"endpoint":    func(w http.ResponseWriter) { writeDBEndpointError(w, busy) },
		"domain":      func(w http.ResponseWriter) { writeDomainError(w, busy) },
		"site":        func(w http.ResponseWriter) { writeSiteError(w, busy) },
		"add db":      func(w http.ResponseWriter) { writeAddDatabaseError(w, "p1", busy) },
		"disk":        func(w http.ResponseWriter) { (&AppDeployHandler{}).writeDiskError(w, busy) },
		"app deploy":  func(w http.ResponseWriter) { (&AppDeployHandler{}).writeError(w, busy) },
		"cluster":     func(w http.ResponseWriter) { writeClusterChangeError(w, "p1", "resize", busy) },
		"read addr":   func(w http.ResponseWriter) { writeDBReadAddressError(w, "p1", busy) },
		"allow list":  func(w http.ResponseWriter) { writeDBAllowListError(w, "p1", busy) },
		"pause":       func(w http.ResponseWriter) { (&ProvisioningHandler{}).writeLifecycleError(w, "p1", busy) },
	}
	for name, write := range writers {
		rec := httptest.NewRecorder()
		write(rec)
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s: status=%d retry-after=%q, want 503 with Retry-After", name, rec.Code, rec.Header().Get("Retry-After"))
		}
	}
}
