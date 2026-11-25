package handler

import (
	"errors"
	"fmt"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/service"
)

// The retrying sentences are our own words, so a customer reads them as they
// are, and a pause that ends in one is still a conflict to come back to.
func TestSelfHealText_CustomerReadsTheRetryingSentences(t *testing.T) {
	for _, sentence := range []error{service.ErrPauseRetrying, service.ErrResumeRetrying} {
		if got := publicFailureMessage(sentence.Error()); got != sentence.Error() {
			t.Errorf("%q withheld from the customer", sentence)
		}
		wrapped := fmt.Errorf("resume p1 at start: pod p1-postgres-1 gone: %w", sentence)
		if got := lifecycleMessage(wrapped); got != sentence.Error() {
			t.Errorf("lifecycle message = %q, want %q", got, sentence)
		}
	}
	if !errors.Is(service.ErrPauseRetrying, service.ErrPauseNotObserved) {
		t.Error("the retrying pause is no longer a pause not observed")
	}
}
