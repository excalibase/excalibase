package handler

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/go-chi/chi/v5"
	_ "github.com/lib/pq"
)

// RealtimeHandler exposes per-project Realtime publication membership
// management. All endpoints connect to the project's Postgres database
// using `excalibase_app` credentials from vault — that role owns the
// publication so it can ALTER ADD/DROP TABLE without superuser access.
type RealtimeHandler struct {
	store           storage.InstanceStore
	orgStore        storage.OrgStore
	vault           vaultclient.VaultClient
	publicationName string
}

func NewRealtimeHandler(store storage.InstanceStore, orgStore storage.OrgStore, vault vaultclient.VaultClient) *RealtimeHandler {
	return &RealtimeHandler{store: store, orgStore: orgStore, vault: vault}
}

// SetPublicationName overrides the default publication name. Must match
// what the watcher daemon and graphql are configured to use, otherwise
// toggle endpoints write to a publication nobody reads.
func (h *RealtimeHandler) SetPublicationName(name string) {
	h.publicationName = name
}

func (h *RealtimeHandler) Routes(r chi.Router) {
	r.Use(auth.RequireAuth)
	r.Get("/tables", h.ListTables)
	r.Put("/tables/{schema}/{table}", h.EnableTable)
	r.Delete("/tables/{schema}/{table}", h.DisableTable)
	r.Post("/enable-all", h.EnableAll)
	r.Post("/disable-all", h.DisableAll)
}

func (h *RealtimeHandler) ListTables(w http.ResponseWriter, r *http.Request) {
	svc, db, err := h.dial(r)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	defer db.Close()

	tables, err := svc.ListTables(r.Context())
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if tables == nil {
		tables = []service.TableState{}
	}
	writeJSON(w, tables)
}

func (h *RealtimeHandler) EnableTable(w http.ResponseWriter, r *http.Request) {
	sch := chi.URLParam(r, "schema")
	tbl := chi.URLParam(r, "table")
	svc, db, err := h.dial(r)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	defer db.Close()
	if err := svc.EnableTable(r.Context(), sch, tbl); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]interface{}{"schema": sch, "table": tbl, "enabled": true})
}

func (h *RealtimeHandler) DisableTable(w http.ResponseWriter, r *http.Request) {
	sch := chi.URLParam(r, "schema")
	tbl := chi.URLParam(r, "table")
	svc, db, err := h.dial(r)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	defer db.Close()
	if err := svc.DisableTable(r.Context(), sch, tbl); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]interface{}{"schema": sch, "table": tbl, "enabled": false})
}

func (h *RealtimeHandler) EnableAll(w http.ResponseWriter, r *http.Request) {
	svc, db, err := h.dial(r)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	defer db.Close()
	count, err := svc.EnableAll(r.Context())
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]int{"added": count})
}

func (h *RealtimeHandler) DisableAll(w http.ResponseWriter, r *http.Request) {
	svc, db, err := h.dial(r)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	defer db.Close()
	count, err := svc.DisableAll(r.Context())
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]int{"dropped": count})
}

// dial resolves the project's excalibase_app credentials from vault
// and opens a short-lived connection. The caller must close the *sql.DB.
// Connection pooling is intentionally minimal — Realtime ops are rare
// (one click per toggle) and the publication owner is granted to
// excalibase_app, so re-dialing per request is fine.
func (h *RealtimeHandler) dial(r *http.Request) (*service.RealtimeService, *sql.DB, error) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		return nil, nil, fmt.Errorf("invalid projectId")
	}
	if _, err := h.store.FindByProjectID(projectID); err != nil {
		return nil, nil, fmt.Errorf("project not found: %w", err)
	}

	// Vault path is project-scoped only — no org dimension to guess at.
	vaultPath := fmt.Sprintf("projects/%s/credentials/excalibase_app", projectID)
	creds, err := h.vault.Get(vaultPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read excalibase_app creds from vault: %w", err)
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		creds["username"], creds["password"], creds["host"], creds["port"], creds["database"])
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	return service.NewRealtimeServiceWithName(db, h.publicationName), db, nil
}

