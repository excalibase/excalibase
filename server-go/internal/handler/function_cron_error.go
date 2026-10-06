package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/scheduler"
	"github.com/lib/pq"
)

// cronSyncFailure is what a deploy whose schedule could not be saved answers:
// a deploy already holding the project's cron lock is a retryable conflict;
// anything else is our failure, told without driver or step names.
func cronSyncFailure(err error) (string, int) {
	if errors.Is(err, scheduler.ErrInvalidSchedule) {
		msg := err.Error()
		if at := strings.Index(msg, "cron job "); at >= 0 {
			msg = msg[at:]
		}
		return msg, http.StatusBadRequest
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "55P03" {
		return "another deploy of this project's functions is in progress; try again in a moment", http.StatusConflict
	}
	return "the function's schedule could not be saved; try again in a moment", http.StatusBadGateway
}
