package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/service"
)

func TestAMissingBackupTargetIsThePlatformsFault(t *testing.T) {
	rec := httptest.NewRecorder()
	if !writeProjectCreationError(rec, fmt.Errorf("provision: %w", service.ErrBackupTargetNotConfigured)) {
		t.Fatal("the refusal was left unanswered")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), service.ErrBackupTargetNotConfigured.Error()) {
		t.Errorf("body = %s", rec.Body.String())
	}
}
