package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A reset that fails on our side tells the user to retry; the step name and
// the driver's text stay in the log.
func TestResetFailureHidesTheCause(t *testing.T) {
	rec := httptest.NewRecorder()
	resetFailure(rec, "update password", errors.New("pq: could not connect to server at 10.42.0.7:5432"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"update password", "pq", "10.42.0.7"} {
		if strings.Contains(body, leak) {
			t.Errorf("body %s leaks %q", body, leak)
		}
	}
	if !strings.Contains(body, "try again") {
		t.Errorf("body %s should tell the user to try again", body)
	}
}
