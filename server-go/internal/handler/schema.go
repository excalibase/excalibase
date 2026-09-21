package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
)

const (
	routeTableRows = "/tables/{tableName}/rows"
	errInvalidBody = "invalid request body"
)

const (
	connTTL     = 10 * time.Minute
	maxConns    = 50
	evictPeriod = 2 * time.Minute
)

type connEntry struct {
	db      *sql.DB
	created time.Time
}

type SchemaHandler struct {
	vault             vaultclient.VaultClient
	introspector      *schema.Introspector
	mu                sync.RWMutex
	connCache         map[string]*connEntry
	dbHostOverride    string // if set, overrides vault host (for local dev with port-forward)
	dbPortOverride    string // if set, overrides vault port
	dbSSLModeOverride string // if set, overrides sslmode (for testing)
	instances         storage.InstanceStore
}

// SetInstanceStore lets the handler resolve a project's instance row.
func (h *SchemaHandler) SetInstanceStore(s storage.InstanceStore) { h.instances = s }

func NewSchemaHandler(v vaultclient.VaultClient) *SchemaHandler {
	h := &SchemaHandler{
		vault:             v,
		introspector:      schema.NewIntrospector(),
		connCache:         make(map[string]*connEntry),
		dbHostOverride:    os.Getenv("SCHEMA_DB_HOST"),
		dbPortOverride:    os.Getenv("SCHEMA_DB_PORT"),
		dbSSLModeOverride: os.Getenv("SCHEMA_DB_SSLMODE"),
	}
	go h.evictLoop()
	return h
}

// ProjectDeleting is the teardown hook (service.DeletionObserver). An open
// session stops the tenant's Postgres shutting down, so its namespace never
// terminates and the teardown times out; the TTL eviction is far too late at
// ten minutes (EXC-431).
func (h *SchemaHandler) ProjectDeleting(projectID string) {
	if err := h.CloseProject(projectID); err != nil {
		log.Printf("WARN: closing the cached connection for %s: %v", projectID, err)
	}
}

// CloseProject releases the connection held for a project, if there is one.
func (h *SchemaHandler) CloseProject(projectID string) error {
	h.mu.Lock()
	entry, held := h.connCache[projectID]
	delete(h.connCache, projectID)
	h.mu.Unlock()

	if !held {
		return nil
	}
	return entry.db.Close()
}

func (h *SchemaHandler) evictLoop() {
	ticker := time.NewTicker(evictPeriod)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		now := time.Now()
		for k, e := range h.connCache {
			if now.Sub(e.created) > connTTL {
				e.db.Close()
				delete(h.connCache, k)
			}
		}
		h.mu.Unlock()
	}
}

func schemaParam(r *http.Request) string {
	s := r.URL.Query().Get("schema")
	if s == "" {
		return "public"
	}
	if err := schema.ValidateSchemaName(s); err != nil {
		return "public"
	}
	return s
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
	cacheKey := projectId
	h.mu.RLock()
	if e, ok := h.connCache[cacheKey]; ok {
		h.mu.RUnlock()
		return e.db, nil
	}
	h.mu.RUnlock()

	// Enforce max connections
	h.mu.RLock()
	count := len(h.connCache)
	h.mu.RUnlock()
	if count >= maxConns {
		return nil, fmt.Errorf("connection pool full (%d)", maxConns)
	}

	// Get excalibase_app credentials from vault. Path is project-scoped only:
	// `projects/{projectId}/credentials/excalibase_app`. Previously this read
	// from `orgs/{orgId}/projects/{projectId}/...` which never matched what
	// the provisioner wrote — a latent bug now fixed alongside the unified
	// vault path scheme.
	creds, err := h.vault.Get(fmt.Sprintf("projects/%s/credentials/excalibase_app", projectId))
	if err != nil {
		return nil, err
	}

	connStr, err := projectdb.DSNFor(creds, projectdb.Overrides{
		Host:    h.dbHostOverride,
		Port:    h.dbPortOverride,
		SSLMode: h.dbSSLModeOverride,
	})
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	h.mu.Lock()
	// Double-check: another goroutine may have cached it while we were connecting
	if existing, ok := h.connCache[cacheKey]; ok {
		h.mu.Unlock()
		db.Close() // close the one we just opened
		return existing.db, nil
	}
	h.connCache[cacheKey] = &connEntry{db: db, created: time.Now()}
	h.mu.Unlock()

	return db, nil
}

func (h *SchemaHandler) handleDBError(w http.ResponseWriter, err error) {
	msg := err.Error()
	if strings.Contains(msg, "sealed") {
		httpError(w, "vault is sealed", http.StatusServiceUnavailable)
	} else if strings.Contains(msg, "not found") {
		httpError(w, "project credentials not found in vault", http.StatusNotFound)
	} else {
		log.Printf("schema db error: %v", err)
		httpError(w, safeError(err), http.StatusInternalServerError)
	}
}
