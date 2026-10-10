package handler

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/go-chi/chi/v5"
)

// MySQLCatalogHandler publishes the MySQL versions a project may be created
// on (ADR 0041), so the create form never carries its own copy. Like the
// Postgres catalogue, the images stay server-side.
type MySQLCatalogHandler struct{}

func NewMySQLCatalogHandler() *MySQLCatalogHandler { return &MySQLCatalogHandler{} }

// Routes mounts the catalogue endpoint (intended under /api/mysql).
func (h *MySQLCatalogHandler) Routes(r chi.Router) {
	r.Get("/catalog", h.List)
}

type mysqlVersionDTO struct {
	Version string `json:"version"`
}

type mysqlCatalogDTO struct {
	// DefaultVersion is what a request that names no version gets.
	DefaultVersion string `json:"defaultVersion"`
	// Versions are newest first, the order a client should offer them.
	Versions []mysqlVersionDTO `json:"versions"`
}

// List returns the catalogue as clients may see it.
func (h *MySQLCatalogHandler) List(w http.ResponseWriter, _ *http.Request) {
	out := mysqlCatalogDTO{DefaultVersion: config.DefaultMySQLVersion()}
	for _, version := range config.MySQLVersions() {
		out.Versions = append(out.Versions, mysqlVersionDTO{Version: version})
	}
	writeJSON(w, out)
}
