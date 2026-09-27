package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/go-chi/chi/v5"
)

const (
	defaultAppLogTail = 200
	maxAppLogTail     = 1000
)

type AppLogReader interface {
	Logs(ctx context.Context, projectID, appID string, opts k8s.AppLogOptions) (k8s.AppLogPage, error)
}

// AppLogHandler serves an app's recent log lines for polling: each answer
// carries the cursor the next request passes as `since`.
type AppLogHandler struct {
	logs AppLogReader
}

func NewAppLogHandler(logs AppLogReader) *AppLogHandler {
	return &AppLogHandler{logs: logs}
}

// Truncated says more lines follow the cursor; the next poll picks them up.
type appLogsResponse struct {
	Lines     []k8s.AppLogLine `json:"lines"`
	Cursor    string           `json:"cursor,omitempty"`
	Truncated bool             `json:"truncated"`
}

func (h *AppLogHandler) Logs(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	appID := chi.URLParam(r, "appId")
	if err := apphost.ValidateID(appID); err != nil {
		httpError(w, "invalid appId", http.StatusBadRequest)
		return
	}
	opts, cursor, err := appLogQuery(r)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	page, err := h.logs.Logs(r.Context(), projectID, appID, opts)
	if errors.Is(err, apphost.ErrAppNotFound) {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("app logs %s/%s: %v", projectID, appID, err)
		httpError(w, "could not read the app's logs", http.StatusInternalServerError)
		return
	}
	lines := page.Lines
	if lines == nil {
		lines = []k8s.AppLogLine{}
	}
	if len(lines) > 0 {
		cursor = lines[len(lines)-1].Time.Format(time.RFC3339Nano)
	}
	writeJSON(w, appLogsResponse{Lines: lines, Cursor: cursor, Truncated: page.Truncated})
}

func appLogQuery(r *http.Request) (k8s.AppLogOptions, string, error) {
	query := r.URL.Query()
	opts := k8s.AppLogOptions{TailLines: defaultAppLogTail}
	cursor := query.Get("since")
	if cursor != "" {
		since, err := time.Parse(time.RFC3339Nano, cursor)
		if err != nil {
			return opts, "", errors.New("since must be an RFC 3339 time")
		}
		opts.Since = &since
	}
	if raw := query.Get("tail"); raw != "" {
		tail, err := strconv.Atoi(raw)
		if err != nil || tail < 1 || tail > maxAppLogTail {
			return opts, "", errors.New("tail must be between 1 and " + strconv.Itoa(maxAppLogTail))
		}
		opts.TailLines = int64(tail)
	}
	if raw := query.Get("previous"); raw != "" {
		previous, err := strconv.ParseBool(raw)
		if err != nil {
			return opts, "", errors.New("previous must be true or false")
		}
		opts.Previous = previous
	}
	return opts, cursor, nil
}
