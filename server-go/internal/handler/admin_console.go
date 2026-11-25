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
	Project(ctx context.Context, actor adminconsole.Actor, projectID, reason string) (*adminconsole.ProjectDetail, error)
}

// ConsoleActions is the one write the console makes.
type ConsoleActions interface {
	Suspend(ctx context.Context, actor adminconsole.Actor, userID string, reason adminconsole.Reason) (*adminconsole.Result, error)
	Unsuspend(ctx context.Context, actor adminconsole.Actor, userID, reason string) (*adminconsole.Result, error)
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
		// Every account's address and sign-in address: only a signed-in Studio
		// session reaches it. A personal access token never does, whatever its
		// scopes, so the console adds no power to a stolen token (EXC-757).
		r.Use(requireSession)
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
	list, ok := parseConsoleList(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	page, err := h.reads.Users(r.Context(), adminconsole.UserQuery{
		Search: list.search, IncludeSuspended: query.Get("includeSuspended") == "true",
		Sort: list.sort, Desc: list.desc, Status: query.Get("status"), Role: query.Get("role"),
		Limit: list.limit, Offset: list.offset,
	})
	if err != nil {
		consoleFailure(w, "users", err)
		return
	}
	writeJSON(w, page)
}

func (h *AdminConsoleHandler) Orgs(w http.ResponseWriter, r *http.Request) {
	list, ok := parseConsoleList(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	page, err := h.reads.Orgs(r.Context(), adminconsole.OrgQuery{
		Search: list.search, IncludeSuspended: query.Get("includeSuspended") == "true",
		Sort: list.sort, Desc: list.desc, Tier: query.Get("tier"), OwnerStatus: query.Get("ownerStatus"),
		Limit: list.limit, Offset: list.offset,
	})
	if err != nil {
		consoleFailure(w, "orgs", err)
		return
	}
	writeJSON(w, page)
}

// consoleList is the part of a list request every console table shares.
type consoleList struct {
	search, sort  string
	desc          bool
	limit, offset int
}

// parseConsoleList reads search, paging and sort; a number or an order it cannot
// read is a 400, not a silent default.
func parseConsoleList(w http.ResponseWriter, r *http.Request) (consoleList, bool) {
	query := r.URL.Query()
	list := consoleList{search: query.Get("q"), sort: query.Get("sort")}
	var err error
	if list.limit, err = optionalInt(query.Get("limit")); err == nil {
		list.offset, err = optionalInt(query.Get("offset"))
	}
	if err == nil {
		list.desc, err = sortDescending(query.Get("order"))
	}
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return list, false
	}
	return list, true
}

// optionalInt reads a number that may be left out; "" is zero, which the
// console reads as "the default".
func optionalInt(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, errors.New("limit and offset must be numbers, zero or more")
	}
	return parsed, nil
}

// sortDescending reads the order; a named sort is ascending unless asked otherwise.
func sortDescending(order string) (bool, error) {
	switch order {
	case "":
		return false, nil
	case "asc", "desc":
		return order == "desc", nil
	}
	return false, errors.New("order must be asc or desc")
}

func (h *AdminConsoleHandler) Project(w http.ResponseWriter, r *http.Request) {
	detail, err := h.reads.Project(r.Context(), consoleActor(r), chi.URLParam(r, "projectId"), r.URL.Query().Get("reason"))
	if err != nil {
		consoleFailure(w, "project", err)
		return
	}
	writeJSON(w, detail)
}

func (h *AdminConsoleHandler) Suspend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason     string `json:"reason"`
		ReasonCode string `json:"reasonCode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	reason := adminconsole.Reason{Code: adminconsole.ReasonCode(body.ReasonCode), Text: body.Reason}
	result, err := h.actions.Suspend(r.Context(), consoleActor(r), chi.URLParam(r, "userId"), reason)
	if err != nil {
		consoleFailure(w, "suspend", err)
		return
	}
	writeJSON(w, result)
}

func (h *AdminConsoleHandler) Unsuspend(w http.ResponseWriter, r *http.Request) {
	reason, ok := decodeReason(w, r)
	if !ok {
		return
	}
	result, err := h.actions.Unsuspend(r.Context(), consoleActor(r), chi.URLParam(r, "userId"), reason)
	if err != nil {
		consoleFailure(w, "unsuspend", err)
		return
	}
	writeJSON(w, result)
}

// decodeReason reads the written reason every console action carries.
func decodeReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return "", false
	}
	return body.Reason, true
}

// requireSession refuses every credential but a signed-in Studio session.
func requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.IsSessionToken(auth.GetToken(r.Context())) {
			httpError(w, "the admin console needs a signed-in session, not an access token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func consoleActor(r *http.Request) adminconsole.Actor {
	actor := adminconsole.Actor{IP: clientIP(r)}
	if user := auth.GetUser(r.Context()); user != nil {
		actor.ID = user.ID
	}
	return actor
}

// consoleFailure answers a refusal in the admin's words and anything else as a
// bare 500: the detail of a database error stays in the log.
func consoleFailure(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, adminconsole.ErrUserNotFound), errors.Is(err, adminconsole.ErrProjectNotFound):
		httpError(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, adminconsole.ErrReasonRequired), errors.Is(err, adminconsole.ErrReasonTooLong), errors.Is(err, adminconsole.ErrReasonCodeUnknown), errors.Is(err, adminconsole.ErrInvalidQuery):
		httpError(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, adminconsole.ErrProtectedAccount):
		httpError(w, err.Error(), http.StatusConflict)
	default:
		log.Printf("ERROR: admin console %s: %v", what, err)
		httpError(w, "admin console request failed", http.StatusInternalServerError)
	}
}
