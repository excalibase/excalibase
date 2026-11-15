package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/service"
)

// A node out of pod slots is the platform's to fix: the caller is told so with a 503, not left with Pending pods.
func TestAProjectRefusedForPodSlotsIsAnsweredWith503(t *testing.T) {
	rec := httptest.NewRecorder()
	if !writeProjectCreationError(rec, fmt.Errorf("provision: %w", service.ErrNoPodSlots)) {
		t.Fatal("the refusal was left unanswered")
	}
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), service.ErrNoPodSlots.Error()) {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestAResumeRefusedForPodSlotsIsAnsweredWith503(t *testing.T) {
	rec := httptest.NewRecorder()
	(&ProvisioningHandler{}).writeLifecycleError(rec, "p1", fmt.Errorf("resume: %w", service.ErrNoPodSlots))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), service.ErrNoPodSlots.Error()) {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}
