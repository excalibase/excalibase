package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/vault"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
)

type SchemaHandler struct {
	vault        *vault.Vault
	introspector *schema.Introspector
	mu           sync.RWMutex
	connCache    map[string]*sql.DB
	dbHostOverride string // if set, overrides vault host (for local dev with port-forward)
	dbPortOverride string // if set, overrides vault port
}

func NewSchemaHandler(v *vault.Vault) *SchemaHandler {
	return &SchemaHandler{
		vault:          v,
		introspector:   schema.NewIntrospector(),
		connCache:      make(map[string]*sql.DB),
		dbHostOverride: os.Getenv("SCHEMA_DB_HOST"),
		dbPortOverride: os.Getenv("SCHEMA_DB_PORT"),
	}
}

func (h *SchemaHandler) Routes(r chi.Router) {
	r.Route("/{projectId}", func(r chi.Router) {
		r.Get("/tables", h.GetTables)
		r.Get("/tables/{tableName}/columns", h.GetColumns)
		r.Get("/relationships", h.GetRelationships)
		r.Get("/tables/{tableName}/indexes", h.GetIndexes)
		r.Post("/ddl", h.ExecuteDDL)
		r.Get("/connection-test", h.TestConnection)
	})
}

func (h *SchemaHandler) GetTables(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	tables, err := h.introspector.GetTables(r.Context(), db, "public")
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
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
	cols, err := h.introspector.GetColumns(r.Context(), db, "public", chi.URLParam(r, "tableName"))
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
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
	rels, err := h.introspector.GetRelationships(r.Context(), db, "public")
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
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
	indexes, err := h.introspector.GetIndexes(r.Context(), db, "public", chi.URLParam(r, "tableName"))
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, indexes)
}

func (h *SchemaHandler) ExecuteDDL(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var body struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	result := h.introspector.ExecuteDDL(r.Context(), db, body.SQL)
	writeJSON(w, result)
}

func (h *SchemaHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	projectId := chi.URLParam(r, "projectId")
	db, err := h.getDB(projectId)
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	connected := h.introspector.TestConnection(r.Context(), db)
	writeJSON(w, map[string]interface{}{
		"connected": connected,
		"projectId": projectId,
	})
}

func (h *SchemaHandler) getDB(projectId string) (*sql.DB, error) {
	h.mu.RLock()
	if db, ok := h.connCache[projectId]; ok {
		h.mu.RUnlock()
		return db, nil
	}
	h.mu.RUnlock()

	// Get excalibase_app credentials from vault
	creds, err := h.vault.Get(fmt.Sprintf("projects/%s/credentials/excalibase_app", projectId))
	if err != nil {
		return nil, err
	}

	host := creds["host"]
	port := creds["port"]
	if h.dbHostOverride != "" {
		host = h.dbHostOverride
	}
	if h.dbPortOverride != "" {
		port = h.dbPortOverride
	}

	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, creds["username"], creds["password"], creds["database"])

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("open connection: %w", err)
	}

	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	h.mu.Lock()
	h.connCache[projectId] = db
	h.mu.Unlock()

	return db, nil
}

func (h *SchemaHandler) handleDBError(w http.ResponseWriter, err error) {
	if err == vault.ErrSealed {
		httpError(w, "vault is sealed", http.StatusServiceUnavailable)
	} else if err == vault.ErrNotFound {
		httpError(w, "project credentials not found in vault", http.StatusNotFound)
	} else {
		httpError(w, err.Error(), http.StatusInternalServerError)
	}
}
