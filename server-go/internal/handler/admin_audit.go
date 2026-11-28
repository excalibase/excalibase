package handler

import (
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/go-chi/chi/v5"
)

// AdminAuditHandler serves the console's audit log and an account's page.
// Both are read-only and name accounts by address, so they have the console's
// gate (manage_orgs on a signed-in session) and one thing more: whoever fails
// the gate gets the answer an unmounted route gives, so the routes are not
// even confirmed to exist.
type AdminAuditHandler struct {
	log      *adminconsole.AuditLog
	accounts *adminconsole.UserDetails
}

func NewAdminAuditHandler(log *adminconsole.AuditLog, accounts *adminconsole.UserDetails) *AdminAuditHandler {
	return &AdminAuditHandler{log: log, accounts: accounts}
}

// AuditRoutes mounts under /api/admin/audit; the caller has authenticated the request.
func (h *AdminAuditHandler) AuditRoutes(r chi.Router) {
	r.Use(staffOnlyOr404)
	r.Get("/", h.List)
}

// AccountRoutes mounts under /api/admin/accounts; the caller has authenticated the request.
func (h *AdminAuditHandler) AccountRoutes(r chi.Router) {
	r.Use(staffOnlyOr404)
	r.Get("/{userId}", h.Account)
}

func (h *AdminAuditHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := optionalInt(r.URL.Query().Get("limit"))
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	filter := adminconsole.AuditFilter{
		Actor:  r.URL.Query().Get("actor"),
		Action: r.URL.Query().Get("action"),
		Target: r.URL.Query().Get("target"),
	}
	page, err := h.log.List(r.Context(), filter, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		consoleFailure(w, "audit log", err)
		return
	}
	writeJSON(w, page)
}

func (h *AdminAuditHandler) Account(w http.ResponseWriter, r *http.Request) {
	detail, err := h.accounts.Detail(r.Context(), chi.URLParam(r, "userId"))
	if err != nil {
		consoleFailure(w, "account detail", err)
		return
	}
	writeJSON(w, detail)
}

// staffOnlyOr404 lets a platform admin on a signed-in session through and
// answers everyone else exactly as the router answers a path it does not have.
func staffOnlyOr404(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := auth.GetUser(r.Context())
		if user == nil || !auth.HasPermission(user.Role, auth.PermManageOrgs) || !auth.IsSessionToken(auth.GetToken(r.Context())) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
