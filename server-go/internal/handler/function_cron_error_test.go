package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lib/pq"
)

// A schedule that could not be saved is told in words a developer can act on;
// the driver's text and our internal step names stay in the log.
func TestCronSyncFailureAnswer(t *testing.T) {
	busy := fmt.Errorf("ensure cron tables: %w", &pq.Error{Code: "55P03", Message: "canceling statement due to lock timeout"})
	msg, code := cronSyncFailure(busy)
	if code != http.StatusConflict {
		t.Errorf("lock timeout: status %d, want 409", code)
	}
	if !strings.Contains(msg, "another deploy") {
		t.Errorf("lock timeout: %q should say another deploy is running", msg)
	}

	msg, code = cronSyncFailure(errors.New("ensure cron tables: pq: connection refused"))
	if code != http.StatusBadGateway {
		t.Errorf("other failure: status %d, want 502", code)
	}
	for _, leak := range []string{"pq", "ensure cron tables", "connection refused"} {
		if strings.Contains(msg, leak) {
			t.Errorf("message %q leaks %q", msg, leak)
		}
	}
}
