package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/service"
)

// A project that cannot get its backup credentials is the platform's fault:
// 503 with a fixed message, never the provider, store or setting behind it.
func TestBackupCredentialFailuresAnswer503WithoutInternals(t *testing.T) {
	for name, err := range map[string]error{
		"no provider":   fmt.Errorf("provision: %w", service.ErrBackupCredentialsNotConfigured),
		"mint failed":   fmt.Errorf("restore src: %w", service.ErrBackupCredentialsUnavailable),
		"prefix in use": fmt.Errorf("%w: proj1/cloud/", service.ErrBackupPrefixInUse),
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if !writeProjectCreationError(w, err) {
				t.Fatal("not answered")
			}
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("status %d, want 503", w.Code)
			}
			body := w.Body.String()
			for _, internal := range []string{"BACKUP_CREDENTIALS_PROVIDER", "proj1/cloud/", "restore src"} {
				if strings.Contains(body, internal) {
					t.Errorf("body leaks %q: %s", internal, body)
				}
			}
		})
	}
}
