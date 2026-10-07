package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
)

const (
	routeTableRows = "/tables/{tableName}/rows"
	errInvalidBody = "invalid request body"
)

// projectPools hands out a bounded pool per project whose sessions carry the
// platform's statement and lock timeouts (projectdb.Opener).
type projectPools interface {
	Open(ctx context.Context, projectID string) (*sql.DB, error)
	Evict(projectID string)
	ProjectStatusChanged(projectID, status string)
}

type SchemaHandler struct {
	pools        projectPools
	introspector *schema.Introspector
	instances    storage.InstanceStore
	publisher    PolicyChangePublisher // optional
}

// SetPublisher wires the bus this handler announces schema changes on. Leave
// it unset and nothing is announced.
func (h *SchemaHandler) SetPublisher(p PolicyChangePublisher) { h.publisher = p }

// AnnounceSchemaChange publishes policies.{projectId}.changed after a request
// that reshaped the schema. The engine caches a project's schema for thirty
// minutes and evicts it on that subject, so without this a table created here
// is invisible until the TTL runs out (EXC-437).
//
// It sits on the route group rather than in each handler: this surface has
// twenty-odd mutating endpoints and the next one added would have been missed
// the same way the first twenty were.
func (h *SchemaHandler) AnnounceSchemaChange(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || h.publisher == nil {
			next.ServeHTTP(w, r)
			return
		}
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if recorder.status >= http.StatusMultipleChoices {
			return
		}
		h.publisher.PublishPolicyChange(r.Context(), domain.PolicyChangeEvent{
			ProjectID: chi.URLParam(r, "projectId"),
			Kind:      domain.SchemaChangeKind,
			Op:        domain.OpChangeUpdate,
		})
	})
}

// statusRecorder remembers the status so the announcement is made only for a
// write the database accepted.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// SetInstanceStore lets the handler resolve a project's instance row.
func (h *SchemaHandler) SetInstanceStore(s storage.InstanceStore) { h.instances = s }

// NewSchemaHandler reaches projects through pools, and bounds every
// statement the SQL runner executes by statementTimeout.
func NewSchemaHandler(pools projectPools, statementTimeout time.Duration) *SchemaHandler {
	return &SchemaHandler{
		pools:        pools,
		introspector: schema.NewIntrospector().WithStatementTimeout(statementTimeout),
	}
}

// ProjectDeleting is the teardown hook (service.DeletionObserver). An open
// session stops the tenant's Postgres shutting down, so its namespace never
// terminates and the teardown times out (EXC-431).
func (h *SchemaHandler) ProjectDeleting(projectID string) {
	h.pools.Evict(projectID)
}

// ProjectStatusChanged is the pause hook: a paused database is down, and its
// pool must not hold connections against it.
func (h *SchemaHandler) ProjectStatusChanged(projectID, status string) {
	h.pools.ProjectStatusChanged(projectID, status)
}

// schemaParam is the ?schema= the route group already validated
// (requireValidSchemaParam), public when absent.
func schemaParam(r *http.Request) string {
	s := r.URL.Query().Get("schema")
	if s == "" {
		return "public"
	}
	return s
}

// requireValidSchemaParam refuses an invalid ?schema= rather than letting a
// DROP aimed at another schema land on public.
func requireValidSchemaParam(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := r.URL.Query().Get("schema"); s != "" {
			if err := schema.ValidateSchemaName(s); err != nil {
				httpError(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Routes mounts the schema endpoints under a /{projectId} segment. Callers that
// already bind {projectId} (and apply RequireProjectAccess) at the mount should
// use RoutesInner instead so the ownership guard runs where {projectId} exists.
func (h *SchemaHandler) Routes(r chi.Router) {
	r.Route("/{projectId}", h.RoutesInner)
}

// RoutesInner registers the schema endpoints relative to an already-bound
// {projectId}. The mount is responsible for RequireProjectAccess (EXC-349).
func (h *SchemaHandler) RoutesInner(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(requireValidSchemaParam)
		h.routes(r)
	})
}

func (h *SchemaHandler) routes(r chi.Router) {
	r.Get("/tables", h.GetTables)
	r.Post("/tables", h.CreateTable)
	r.Patch("/tables/{tableName}", h.UpdateTable)
	r.Delete("/tables/{tableName}", h.DropTable)
	r.Get("/tables/{tableName}/columns", h.GetColumns)
	r.Post("/tables/{tableName}/columns", h.AddColumn)
	r.Patch("/tables/{tableName}/columns/{columnName}", h.AlterColumn)
	r.Delete("/tables/{tableName}/columns/{columnName}", h.DropColumn)
	r.Get("/relationships", h.GetRelationships)
	r.Get("/tables/{tableName}/indexes", h.GetIndexes)
	r.Get("/tables/{tableName}/checks", h.GetCheckConstraints)
	r.Post("/ddl", h.ExecuteDDL)
	r.Post("/query", h.ExecuteQuery)
	r.Get("/connection-test", h.TestConnection)
	r.Get("/roles", h.GetRoles)
	r.Post("/roles", h.CreateRole)
	r.Delete("/roles/{roleName}", h.DropRole)
	r.Get("/extensions", h.GetExtensions)
	r.Post("/extensions", h.CreateExtension)
	r.Delete("/extensions/{extName}", h.DropExtension)
	r.Get("/policies", h.GetPolicies)
	r.Post("/policies", h.CreatePolicy)
	r.Delete("/policies/{policyName}", h.DropPolicy)
	r.Get("/functions", h.GetFunctions)
	r.Post("/functions", h.CreateFunction)
	r.Delete("/functions/{funcName}", h.DropFunction)
	r.Get("/triggers", h.GetTriggers)
	r.Post("/triggers", h.CreateTrigger)
	r.Delete("/triggers/{triggerName}", h.DropTrigger)
	r.Post("/indexes", h.CreateIndex)
	r.Delete("/indexes/{indexName}", h.DropIndex)
	r.Get("/types", h.GetTypes)
	r.Get(routeTableRows, h.GetRows)
	r.Post(routeTableRows, h.InsertRow)
	r.Patch(routeTableRows, h.UpdateRow)
	r.Delete(routeTableRows, h.DeleteRow)
	r.Get("/advisors/performance", h.RunPerformanceAdvisor)
	r.Get("/advisors/security", h.RunSecurityAdvisor)
}

// --- Tables ---

func (h *SchemaHandler) GetTables(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	tables, err := h.introspector.GetTables(r.Context(), db, schemaParam(r))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, tables)
}

func (h *SchemaHandler) GetColumns(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	cols, err := h.introspector.GetColumns(r.Context(), db, schemaParam(r), chi.URLParam(r, "tableName"))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, cols)
}

func (h *SchemaHandler) GetRelationships(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	rels, err := h.introspector.GetRelationships(r.Context(), db, schemaParam(r))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, rels)
}

func (h *SchemaHandler) GetIndexes(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	indexes, err := h.introspector.GetIndexes(r.Context(), db, schemaParam(r), chi.URLParam(r, "tableName"))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, indexes)
}

func (h *SchemaHandler) GetCheckConstraints(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	checks, err := h.introspector.GetCheckConstraints(r.Context(), db, schemaParam(r), chi.URLParam(r, "tableName"))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, checks)
}

func (h *SchemaHandler) CreateTable(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var req schema.CreateTableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		httpError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := h.introspector.CreateTable(r.Context(), db, req); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (h *SchemaHandler) UpdateTable(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var req schema.UpdateTableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	tableName := chi.URLParam(r, "tableName")
	if err := h.introspector.UpdateTable(r.Context(), db, schemaParam(r), tableName, req); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (h *SchemaHandler) DropTable(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	tableName := chi.URLParam(r, "tableName")
	cascade := r.URL.Query().Get("cascade") == "true"
	if err := h.introspector.DropTable(r.Context(), db, schemaParam(r), tableName, cascade); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "dropped"})
}

// --- Columns ---

func (h *SchemaHandler) AddColumn(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var req schema.AddColumnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Type == "" {
		httpError(w, "name and type are required", http.StatusBadRequest)
		return
	}
	tableName := chi.URLParam(r, "tableName")
	if err := h.introspector.AddColumn(r.Context(), db, schemaParam(r), tableName, req); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (h *SchemaHandler) AlterColumn(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var req schema.AlterColumnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	tableName := chi.URLParam(r, "tableName")
	columnName := chi.URLParam(r, "columnName")
	if err := h.introspector.AlterColumn(r.Context(), db, schemaParam(r), tableName, columnName, req); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (h *SchemaHandler) DropColumn(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	tableName := chi.URLParam(r, "tableName")
	columnName := chi.URLParam(r, "columnName")
	if err := h.introspector.DropColumn(r.Context(), db, schemaParam(r), tableName, columnName); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "dropped"})
}

// --- Internal helpers ---

// indexOfByte returns the index of the first occurrence of sep in s, or -1.
func indexOfByte(s string, sep byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return i
		}
	}
	return -1
}

func (h *SchemaHandler) getDB(projectId string) (*sql.DB, error) {
	if !isValidID(projectId) {
		return nil, fmt.Errorf("invalid project id")
	}
	return h.pools.Open(context.Background(), projectId)
}

func (h *SchemaHandler) handleDBError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, projectdb.ErrNotServable):
		httpError(w, "project is not running", http.StatusConflict)
	case errors.Is(err, domain.ErrNoDatabase):
		httpError(w, domain.ErrNoDatabase.Error(), http.StatusConflict)
	case strings.Contains(msg, "unknown project"):
		httpError(w, "project not found", http.StatusNotFound)
	case strings.Contains(msg, "sealed"):
		httpError(w, "vault is sealed", http.StatusServiceUnavailable)
	case strings.Contains(msg, "not found"):
		httpError(w, "project credentials not found in vault", http.StatusNotFound)
	default:
		log.Printf("schema db error: %v", err)
		httpError(w, safeError(err), http.StatusInternalServerError)
	}
}
