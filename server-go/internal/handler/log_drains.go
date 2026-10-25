package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// Log drains (EXC-658): a project's Admins send its logs to Datadog or an
// HTTPS endpoint. The key is write-only: stored in the vault, never returned.

const maxLogDrainBodyBytes = 16 << 10

// LogDrainManager is the service the drain routes call.
type LogDrainManager interface {
	List(ctx context.Context, projectID string) ([]domain.LogDrain, error)
	HasSecret(drain *domain.LogDrain) (bool, error)
	Create(ctx context.Context, projectID, actor string, input service.CreateLogDrainInput) (*domain.LogDrain, error)
	Update(ctx context.Context, projectID, id string, input service.UpdateLogDrainInput) (*domain.LogDrain, error)
	Delete(ctx context.Context, projectID, id string) error
}

type LogDrainsHandler struct {
	drains LogDrainManager
	audit  auditWriter
}

// NewLogDrainsHandler takes an optional audit log; nil records nothing.
func NewLogDrainsHandler(drains LogDrainManager, audit auditWriter) *LogDrainsHandler {
	return &LogDrainsHandler{drains: drains, audit: audit}
}

func (h *LogDrainsHandler) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Patch("/{drainId}", h.update)
	r.Delete("/{drainId}", h.remove)
}

type logDrainView struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Kind                string     `json:"kind"`
	DatadogSite         string     `json:"datadogSite,omitempty"`
	URL                 string     `json:"url,omitempty"`
	HeaderName          string     `json:"headerName,omitempty"`
	HasSecret           bool       `json:"hasSecret"`
	Enabled             bool       `json:"enabled"`
	Status              string     `json:"status"`
	LastError           string     `json:"lastError,omitempty"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	LastSuccessAt       *time.Time `json:"lastSuccessAt,omitempty"`
	SentLines           int64      `json:"sentLines"`
	DroppedLines        int64      `json:"droppedLines"`
	CreatedAt           time.Time  `json:"createdAt"`
}

func (h *LogDrainsHandler) view(drain *domain.LogDrain) logDrainView {
	hasSecret, err := h.drains.HasSecret(drain)
	if err != nil {
		log.Printf("log drain %s: key state: %v", drain.ID, err)
	}
	return logDrainView{ID: drain.ID, Name: drain.Name, Kind: drain.Kind, DatadogSite: drain.DatadogSite, URL: drain.URL,
		HeaderName: drain.HeaderName, HasSecret: hasSecret, Enabled: drain.Enabled, Status: drain.Status,
		LastError: drain.LastError, ConsecutiveFailures: drain.ConsecutiveFailures, LastSuccessAt: drain.LastSuccessAt,
		SentLines: drain.SentLines, DroppedLines: drain.DroppedLines, CreatedAt: drain.CreatedAt}
}

func (h *LogDrainsHandler) list(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	drains, err := h.drains.List(r.Context(), projectID)
	if err != nil {
		log.Printf("log drains of %s: %v", safeLog(projectID), err)
		httpError(w, "log drains could not be read", http.StatusInternalServerError)
		return
	}
	views := make([]logDrainView, 0, len(drains))
	for i := range drains {
		views = append(views, h.view(&drains[i]))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, views)
}

type createLogDrainRequest struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	DatadogSite string `json:"datadogSite"`
	URL         string `json:"url"`
	HeaderName  string `json:"headerName"`
	Secret      string `json:"secret"`
}

type updateLogDrainRequest struct {
	Name        *string `json:"name"`
	DatadogSite *string `json:"datadogSite"`
	URL         *string `json:"url"`
	HeaderName  *string `json:"headerName"`
	Secret      *string `json:"secret"`
	Enabled     *bool   `json:"enabled"`
}

func (h *LogDrainsHandler) create(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var body createLogDrainRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	drain, err := h.drains.Create(r.Context(), projectID, actorID(r), service.CreateLogDrainInput{
		Name: body.Name, Kind: body.Kind, DatadogSite: body.DatadogSite, URL: body.URL, HeaderName: body.HeaderName, Secret: body.Secret})
	if err != nil {
		writeLogDrainError(w, projectID, err)
		return
	}
	h.record(r, "log_drain.create", drain)
	writeJSONStatus(w, http.StatusCreated, h.view(drain))
}

func (h *LogDrainsHandler) update(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var body updateLogDrainRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	drain, err := h.drains.Update(r.Context(), projectID, chi.URLParam(r, "drainId"), service.UpdateLogDrainInput{
		Name: body.Name, DatadogSite: body.DatadogSite, URL: body.URL, HeaderName: body.HeaderName, Secret: body.Secret, Enabled: body.Enabled})
	if err != nil {
		writeLogDrainError(w, projectID, err)
		return
	}
	h.record(r, "log_drain.update", drain)
	writeJSON(w, h.view(drain))
}

func (h *LogDrainsHandler) remove(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	drainID := chi.URLParam(r, "drainId")
	if err := h.drains.Delete(r.Context(), projectID, drainID); err != nil {
		writeLogDrainError(w, projectID, err)
		return
	}
	h.record(r, "log_drain.delete", &domain.LogDrain{ID: drainID, ProjectID: projectID})
	w.WriteHeader(http.StatusNoContent)
}

func writeLogDrainError(w http.ResponseWriter, projectID string, err error) {
	switch {
	case errors.Is(err, logdrain.ErrInvalidDrain):
		httpError(w, strings.TrimPrefix(err.Error(), logdrain.ErrInvalidDrain.Error()+": "), http.StatusBadRequest)
	case errors.Is(err, domain.ErrLogDrainLimit):
		httpError(w, fmt.Sprintf("a project has at most %d log drains", service.MaxLogDrainsPerProject), http.StatusConflict)
	case errors.Is(err, service.ErrLogDrainsUnavailable):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, domain.ErrLogDrainNotFound):
		httpError(w, domain.ErrLogDrainNotFound.Error(), http.StatusNotFound)
	case errors.Is(err, service.ErrProjectNotFound):
		httpError(w, "project not found", http.StatusNotFound)
	default:
		log.Printf("log drains of %s: %v", safeLog(projectID), err)
		httpError(w, "the log drain could not be saved", http.StatusInternalServerError)
	}
}

// record audits a drain change by its destination, never its key.
func (h *LogDrainsHandler) record(r *http.Request, action string, drain *domain.LogDrain) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{"name": drain.Name, "kind": drain.Kind, "datadogSite": drain.DatadogSite,
		"host": endpointHost(drain.URL), "headerName": drain.HeaderName, "enabled": drain.Enabled})
	now := time.Now()
	entry := &domain.AuditEntry{UserID: actorID(r), Action: action, Resource: "log_drain", ResourceID: drain.ID,
		ProjectID: drain.ProjectID, Details: string(details), IPAddress: clientIP(r), Timestamp: &now}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", action, drain.ID, err)
	}
}

// endpointHost keeps an audit entry free of a token in the URL's path or query.
func endpointHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// LogForwarderAPI is what the forwarder pod calls.
type LogForwarderAPI interface {
	Pull(ctx context.Context, max int) ([]logdrain.Batch, error)
	Ack(ctx context.Context, ack logdrain.Ack) error
}

// LogForwarderHandler serves the forwarder pod, which holds the shared
// forwarder token; nothing else may pull tenants' keys.
type LogForwarderHandler struct {
	api       LogForwarderAPI
	tokenHash [32]byte
	hasToken  bool
}

func NewLogForwarderHandler(api LogForwarderAPI, token string) *LogForwarderHandler {
	return &LogForwarderHandler{api: api, tokenHash: sha256.Sum256([]byte(token)), hasToken: token != ""}
}

// Mount serves the forwarder's routes; gates run before the token check, so
// a refused caller learns nothing about tokens.
func (h *LogForwarderHandler) Mount(r chi.Router, gates ...func(http.Handler) http.Handler) {
	chain := append(append([]func(http.Handler) http.Handler{}, gates...), h.requireToken)
	r.With(chain...).Post(logdrain.PullPath, h.pull)
	r.With(chain...).Post(logdrain.AckPath, h.ack)
}

// requireToken compares hashes in constant time; with no token configured
// every call is refused.
func (h *LogForwarderHandler) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := r.Header.Get(logdrain.TokenHeader)
		sum := sha256.Sum256([]byte(presented))
		if !h.hasToken || presented == "" || subtle.ConstantTimeCompare(sum[:], h.tokenHash[:]) != 1 {
			httpError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *LogForwarderHandler) pull(w http.ResponseWriter, r *http.Request) {
	asked := logdrain.PullRequest{Max: logdrain.MaxDrainsPerPull}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLogDrainBodyBytes)).Decode(&asked); err != nil && !errors.Is(err, io.EOF) {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	batches, err := h.api.Pull(r.Context(), asked.Max)
	if err != nil {
		log.Printf("log forwarder pull: %v", err)
		httpError(w, "pull failed", http.StatusInternalServerError)
		return
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	// Unescaped, the answer is the size provisioning budgeted for.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(logdrain.PullResponse{Batches: batches}); err != nil {
		httpError(w, "pull failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body.Bytes())
}

func (h *LogForwarderHandler) ack(w http.ResponseWriter, r *http.Request) {
	var ack logdrain.Ack
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLogDrainBodyBytes)).Decode(&ack); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	if err := h.api.Ack(r.Context(), ack); err != nil {
		if errors.Is(err, service.ErrInvalidLogDrainAck) {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("log forwarder ack: %v", err)
		httpError(w, "ack failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
