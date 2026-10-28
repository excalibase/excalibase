package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// mountDBReadAddress serves a database's read-only address (EXC-666): reads
// Developer+, opening its public port Admin+; unmounted while the flag is off.
func mountDBReadAddress(r *chi.Mux, sqlStore storage.OrgStore, store storage.InstanceStore, d *handlerDeps) {
	readAddress := d.dbReadAddressHandler
	if readAddress == nil {
		readAddress = handler.NewDBReadAddressHandler(nil, nil)
	}
	r.Route("/api/projects/{projectId}/db-read-address", func(r chi.Router) {
		r.Use(readAddressGate(d.features))
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Use(custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, sqlStore))
		r.Use(custommw.RequireProjectRoleForWrites(domain.OrgRoleAdmin, store, sqlStore))
		r.Use(custommw.RequireProjectDatabase(store))
		r.Use(d.activity)
		readAddress.Routes(r)
	})
}

// newDBReadAddressHandler starts without its service, which is wired once built.
func newDBReadAddressHandler(sqlStore any) *handler.DBReadAddressHandler {
	audit, _ := sqlStore.(interface {
		LogAudit(ctx context.Context, entry *domain.AuditEntry) error
	})
	return handler.NewDBReadAddressHandler(nil, audit)
}

// readAddressGate answers as unmounted while the flag is off, except a PUT
// closing the read port: a port opened before a rollback can still be closed.
func readAddressGate(flags features.Flags) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if features.Enabled(r.Context(), flags, features.DBReadAddress) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method != http.MethodPut {
				http.NotFound(w, r)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
			var close struct {
				Enabled *bool `json:"enabled"`
			}
			if err != nil || json.Unmarshal(body, &close) != nil || close.Enabled == nil || *close.Enabled {
				http.NotFound(w, r)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}
