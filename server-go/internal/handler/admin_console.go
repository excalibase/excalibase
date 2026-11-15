package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/go-chi/chi/v5"
)

// ConsoleReads is what the console's pages read.
type ConsoleReads interface {
	Users(ctx context.Context, query adminconsole.UserQuery) (adminconsole.Page[adminconsole.UserRow], error)
	Orgs(ctx context.Context, query adminconsole.OrgQuery) (adminconsole.Page[adminconsole.OrgRow], error)
	Overview(ctx context.Context) (*adminconsole.Overview, error)
	Project(ctx context.Context, projectID string) (*adminconsole.ProjectDetail, error)
}

// ConsoleActions is the one write the console makes.
type ConsoleActions interface {
	Suspend(ctx context.Context, actor adminconsole.Actor, userID, reason string) (*adminconsole.Result, error)
	Unsuspend(ctx context.Context, actor adminconsole.Actor, userID string) (*adminconsole.Result, error)
}

// AdminConsoleHandler serves the platform admin console (EXC-757). Every route
// needs manage_orgs, which only platform_admin holds: the pages show other
// people's email addresses and sign-in addresses, so a read-only platform role
// is not enough.
type AdminConsoleHandler struct {
	reads   ConsoleReads
	actions ConsoleActions
}

func NewAdminConsoleHandler(reads ConsoleReads, actions ConsoleActions) *AdminConsoleHandler {
	return &AdminConsoleHandler{reads: reads, actions: actions}
}

// Routes mounts under /api/admin; the caller has authenticated the request.
func (h *AdminConsoleHandler) Routes(r chi.Router) {
	r.Route("/console", func(r chi.Router) {
		r.Use(auth.RequirePermission(auth.PermManageOrgs))
		// Every account's address and sign-in address: a token narrowed to one
		// project or one scope must not reach it, even a platform admin's.
		r.Use(auth.RequireUnrestrictedCredential)
		r.Get("/overview", h.Overview)
		r.Get("/users", h.Users)
		r.Get("/orgs", h.Orgs)
		r.Get("/projects/{projectId}", h.Project)
		r.Post("/users/{userId}/suspend", h.Suspend)
		r.Post("/users/{userId}/unsuspend", h.Unsuspend)
	})
}

func (h *AdminConsoleHandler) Overview(w http.ResponseWriter, r *http.Request) {
	overview, err := h.reads.Overview(r.Context())
	if err != nil {
		consoleFailure(w, "overview", err)
		return
	}
	writeJSON(w, overview)
}

func (h *AdminConsoleHandler) Users(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	page, err := h.reads.Users(r.Context(), adminconsole.UserQuery{
		Search: query.Get("q"), IncludeSuspended: query.Get("includeSuspended") == "true",
		Limit: intParam(query.Get("limit")), Offset: intParam(query.Get("offset")),
	})
	if err != nil {
		consoleFailure(w, "users", err)
		return
	}
	writeJSON(w, page)
}

func (h *AdminConsoleHandler) Orgs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	page, err := h.reads.Orgs(r.Context(), adminconsole.OrgQuery{
		Search: query.Get("q"), IncludeSuspended: query.Get("includeSuspended") == "true",
		Limit: intParam(query.Get("limit")), Offset: intParam(query.Get("offset")),
	})
	if err != nil {
		consoleFailure(w, "orgs", err)
		return
	}
	writeJSON(w, page)
}

func (h *AdminConsoleHandler) Project(w http.ResponseWriter, r *http.Request) {
	detail, err := h.reads.Project(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		consoleFailure(w, "project", err)
		return
	}
	writeJSON(w, detail)
}

func (h *AdminConsoleHandler) Suspend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	result, err := h.actions.Suspend(r.Context(), consoleActor(r), chi.URLParam(r, "userId"), body.Reason)
	if err != nil {
		consoleFailure(w, "suspend", err)
		return
	}
	writeJSON(w, result)
}

func (h *AdminConsoleHandler) Unsuspend(w http.ResponseWriter, r *http.Request) {
	result, err := h.actions.Unsuspend(r.Context(), consoleActor(r), chi.URLParam(r, "userId"))
	if err != nil {
		consoleFailure(w, "unsuspend", err)
		return
	}
	writeJSON(w, result)
}

func consoleActor(r *http.Request) adminconsole.Actor {
	actor := adminconsole.Actor{IP: clientIP(r)}
	if user := auth.GetUser(r.Context()); user != nil {
		actor.ID = user.ID
	}
	return actor
}

func intParam(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

// consoleFailure answers a refusal in the admin's words and anything else as a
// bare 500: the detail of a database error stays in the log.
func consoleFailure(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, adminconsole.ErrUserNotFound), errors.Is(err, adminconsole.ErrProjectNotFound):
		httpError(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, adminconsole.ErrReasonRequired), errors.Is(err, adminconsole.ErrReasonTooLong):
		httpError(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, adminconsole.ErrProtectedAccount):
		httpError(w, err.Error(), http.StatusConflict)
	default:
		log.Printf("ERROR: admin console %s: %v", what, err)
		httpError(w, "admin console request failed", http.StatusInternalServerError)
	}
}
