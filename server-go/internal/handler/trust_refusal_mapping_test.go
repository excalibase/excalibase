package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/trust"
)

// EXC-899: a service that refuses a limited owner's container is answered 403
// trust_required by every handler that reaches one, not a 500.
func TestContainerHandlers_MapTheVerificationRefusal(t *testing.T) {
	refusal := fmt.Errorf("wrapped: %w", &trust.VerificationRequiredError{Message: "verify with GitHub first"})
	writers := map[string]func(http.ResponseWriter, error){
		"app deploy": func(w http.ResponseWriter, err error) { (&AppDeployHandler{}).writeError(w, err) },
		"template":   writeTemplateError,
		"site":       writeSiteError,
		"site git":   writeSiteGitError,
		"app git":    writeAppGitError,
	}
	for name, write := range writers {
		w := httptest.NewRecorder()
		write(w, refusal)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"trust_required"`) || !strings.Contains(w.Body.String(), "verify with GitHub first") {
			t.Errorf("%s: %d %s, want 403 trust_required with the message", name, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		write(w, fmt.Errorf("%w: %w", trust.ErrCheckUnavailable, fmt.Errorf("db down")))
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "db down") {
			t.Errorf("%s: unreadable check: %d %s, want 503 without the cause", name, w.Code, w.Body.String())
		}
	}
}
