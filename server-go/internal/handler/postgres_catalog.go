package handler

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/go-chi/chi/v5"
)

// PostgresCatalogHandler publishes the PostgreSQL image catalogue to clients
// that have to offer a choice of major — the Studio create-project form above
// all.
//
// It exists so that no client carries its own copy of the supported set or of
// the DocumentDB rule. Both move when the catalogue moves (a major is added,
// one is withdrawn, an image is published), and a client that hardcoded either
// would go on offering what the platform no longer provisions, or refusing
// what it now does.
//
// What is published is deliberately narrower than the catalogue file: a
// customer needs to know which majors they may choose, which of them carry
// DocumentDB and why the rest do not. Which image serves a major is the
// platform's business, so the digest-pinned references stay server-side.
type PostgresCatalogHandler struct{}

func NewPostgresCatalogHandler() *PostgresCatalogHandler { return &PostgresCatalogHandler{} }

// Routes mounts the catalogue endpoint (intended under /api/postgres).
func (h *PostgresCatalogHandler) Routes(r chi.Router) {
	r.Get("/catalog", h.List)
}

type postgresMajorDTO struct {
	// Major is the bare major version, in the catalogue's own spelling, and is
	// exactly what a provisioning request must name.
	Major string `json:"major"`
	// Available reports whether the platform can provision this major right
	// now. A catalogued major whose image has not been published yet is a real
	// state, and a form that offered it anyway would collect a choice the API
	// then refuses.
	Available bool `json:"available"`
	// DocumentDB reports whether this major's image carries the DocumentDB
	// extension.
	DocumentDB bool `json:"documentDb"`
	// DocumentDBUnavailableReason is set exactly when DocumentDB is false, so
	// a client can show the customer why the option is closed to them before
	// they choose rather than after they submit.
	DocumentDBUnavailableReason string `json:"documentDbUnavailableReason,omitempty"`
}

type postgresCatalogDTO struct {
	// DocumentDBRef is the upstream DocumentDB tag the images are built from.
	DocumentDBRef string `json:"documentDbRef,omitempty"`
	// Majors is in catalogue order, which is the order a client should offer.
	Majors []postgresMajorDTO `json:"majors"`
}

// documentDBUnavailableReason explains a catalogue entry that carries no
// DocumentDB. The catalogue decides which majors those are; this only renders
// the decision into a sentence a customer can act on.
func documentDBUnavailableReason(major string) string {
	available := config.DocumentDBMajorsMessage()
	if available == "" {
		return "DocumentDB is not available on PostgreSQL " + major + "."
	}
	return "DocumentDB is not available on PostgreSQL " + major +
		". The extension is built only for PostgreSQL " + available +
		", and it is fixed when the project is created."
}

// List returns the catalogue as clients may see it.
func (h *PostgresCatalogHandler) List(w http.ResponseWriter, _ *http.Request) {
	entries := config.PostgresCatalogEntries()
	out := postgresCatalogDTO{
		DocumentDBRef: config.DocumentDBRef(),
		Majors:        make([]postgresMajorDTO, 0, len(entries)),
	}
	for _, entry := range entries {
		major := postgresMajorDTO{
			Major:      entry.Major,
			Available:  entry.Image != "",
			DocumentDB: entry.DocumentDB,
		}
		if !entry.DocumentDB {
			major.DocumentDBUnavailableReason = documentDBUnavailableReason(entry.Major)
		}
		out.Majors = append(out.Majors, major)
	}
	writeJSON(w, out)
}
