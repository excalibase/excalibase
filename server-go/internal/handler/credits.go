package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	creditsRequestLimit    = 4096
	idempotencyKeyHeader   = "Idempotency-Key"
	statementMonthLayout   = "2006-01"
	errCreditsUnavailable  = "credits are unavailable"
	errTermsNeedSession    = "accepting the credit terms needs a signed-in session, not an access token"
	errIdempotencyKeyShape = "an Idempotency-Key header of 8 to 128 letters, digits, dots, dashes, underscores or colons is required"
)

var (
	idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	unsafeFileNameChars   = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

// creditMembers is how the credit routes tell who belongs to an org.
type creditMembers interface {
	GetOrgMember(ctx context.Context, orgID, userID string) (*domain.OrgMember, error)
}

// CreditsHandler serves an org's credit balance, statement and terms, and the
// admin's grants (EXC-866). Its routes are mounted behind the credits feature gate.
type CreditsHandler struct {
	service  *credits.Service
	members  creditMembers
	termsURL string
}

func NewCreditsHandler(service *credits.Service, members creditMembers, termsURL string) *CreditsHandler {
	return &CreditsHandler{service: service, members: members, termsURL: termsURL}
}

// OrgRoutes mounts the member reads under /api/orgs/{orgId}.
func (h *CreditsHandler) OrgRoutes(r chi.Router, gate func(http.Handler) http.Handler) {
	r.With(gate).Route("/credits", func(r chi.Router) {
		r.Get("/", h.Overview)
		r.Get("/entries", h.Entries)
		r.Get("/statement", h.Statement)
	})
}

// ConsoleRoutes mounts the admin grant under /api/admin.
func (h *CreditsHandler) ConsoleRoutes(r chi.Router, gate func(http.Handler) http.Handler) {
	r.With(gate, auth.RequirePermission(auth.PermManageOrgs), requireSession).Post("/console/orgs/{orgId}/credits", h.AdminAdjust)
}

// PersonRoutes mounts what a person does for themselves under /api/credits.
func (h *CreditsHandler) PersonRoutes(r chi.Router, gate func(http.Handler) http.Handler) {
	r.With(gate, requireSessionFor(errTermsNeedSession)).Post("/terms", h.AcceptTerms)
}

func requireSessionFor(message string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !auth.IsSessionToken(auth.GetToken(r.Context())) {
				httpError(w, message, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// member answers 404 for anyone outside the org, so the id confirms nothing.
func (h *CreditsHandler) member(w http.ResponseWriter, r *http.Request) (orgID string, user *domain.User, ok bool) {
	user = auth.GetUser(r.Context())
	orgID = chi.URLParam(r, "orgId")
	if user == nil {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return "", nil, false
	}
	if auth.IsPlatformServicePrincipal(user, auth.GetToken(r.Context())) {
		return orgID, user, true
	}
	member, err := h.members.GetOrgMember(r.Context(), orgID, user.ID)
	if err != nil || member == nil {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return "", nil, false
	}
	return orgID, user, true
}

type creditsOverviewBody struct {
	credits.Overview
	TermsURL string `json:"termsUrl"`
}

func (h *CreditsHandler) Overview(w http.ResponseWriter, r *http.Request) {
	orgID, user, ok := h.member(w, r)
	if !ok {
		return
	}
	overview, err := h.service.Overview(r.Context(), orgID, user.ID)
	if err != nil {
		creditsFailure(w, "read credits", err)
		return
	}
	writeJSON(w, creditsOverviewBody{Overview: overview, TermsURL: h.termsURL})
}

func (h *CreditsHandler) Entries(w http.ResponseWriter, r *http.Request) {
	orgID, _, ok := h.member(w, r)
	if !ok {
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := h.service.Entries(r.Context(), orgID, max(before, 0), limit)
	if err != nil {
		creditsFailure(w, "read credit entries", err)
		return
	}
	writeJSON(w, map[string]any{"entries": entries})
}

// Statement is one UTC month, as JSON or, with format=csv, a download.
func (h *CreditsHandler) Statement(w http.ResponseWriter, r *http.Request) {
	orgID, _, ok := h.member(w, r)
	if !ok {
		return
	}
	month := time.Now().UTC()
	if raw := r.URL.Query().Get("month"); raw != "" {
		parsed, err := time.Parse(statementMonthLayout, raw)
		if err != nil {
			httpError(w, "month must look like 2026-12", http.StatusBadRequest)
			return
		}
		month = parsed
	}
	statement, err := h.service.Statement(r.Context(), orgID, month)
	if err != nil {
		creditsFailure(w, "read the credit statement", err)
		return
	}
	if r.URL.Query().Get("format") != "csv" {
		writeJSON(w, statement)
		return
	}
	name := unsafeFileNameChars.ReplaceAllString(fmt.Sprintf("excalibase-credits-%s-%s.csv", orgID, statement.Month), "_")
	w.Header().Set(hdrContentType, "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	if err := credits.WriteStatementCSV(w, statement); err != nil {
		log.Printf("ERROR: credit statement csv %s: %v", orgID, err)
	}
}

func (h *CreditsHandler) AcceptTerms(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, creditsRequestLimit)).Decode(&body); err != nil {
		httpError(w, errInvalidRequest, http.StatusBadRequest)
		return
	}
	result, err := h.service.AcceptTerms(r.Context(), user.ID, body.Version)
	if errors.Is(err, credits.ErrTermsVersion) {
		httpError(w, "these are not the current credit terms; reload the page", http.StatusConflict)
		return
	}
	if err != nil {
		creditsFailure(w, "accept the credit terms", err)
		return
	}
	writeJSON(w, map[string]any{"accepted": true, "outcome": result.Outcome})
}

type adminCreditBody struct {
	AmountMicros int64      `json:"amountMicros"`
	Reason       string     `json:"reason"`
	ExpiresAt    *time.Time `json:"expiresAt"`
}

// AdminAdjust adds credit to an org, or takes some back, with a written
// reason; its audit row is written in the same transaction as the entry.
func (h *CreditsHandler) AdminAdjust(w http.ResponseWriter, r *http.Request) {
	var body adminCreditBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, creditsRequestLimit)).Decode(&body); err != nil {
		httpError(w, errInvalidRequest, http.StatusBadRequest)
		return
	}
	key := r.Header.Get(idempotencyKeyHeader)
	if !idempotencyKeyPattern.MatchString(key) {
		httpError(w, errIdempotencyKeyShape, http.StatusBadRequest)
		return
	}
	actor := ""
	if user := auth.GetUser(r.Context()); user != nil {
		actor = user.ID
	}
	entry, err := h.service.Adjust(r.Context(), credits.AdminChange{
		OrgID: chi.URLParam(r, "orgId"), ActorID: actor, AmountMicros: credits.Micros(body.AmountMicros), Reason: body.Reason,
		IdempotencyKey: key, ExpiresAt: body.ExpiresAt, IPAddress: clientIP(r),
	})
	if err != nil {
		adminCreditFailure(w, err)
		return
	}
	writeJSON(w, entry)
}

func adminCreditFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, credits.ErrReasonLength), errors.Is(err, credits.ErrInvalidAmount), errors.Is(err, credits.ErrAboveCap),
		errors.Is(err, credits.ErrPastExpiry), errors.Is(err, credits.ErrMissingKey):
		httpError(w, strings.TrimPrefix(err.Error(), "credits: "), http.StatusBadRequest)
	case errors.Is(err, credits.ErrOrgNotFound):
		httpError(w, errOrgNotFound, http.StatusNotFound)
	case errors.Is(err, credits.ErrInsufficientBalance), errors.Is(err, credits.ErrIdempotencyConflict):
		httpError(w, strings.TrimPrefix(err.Error(), "credits: "), http.StatusConflict)
	default:
		creditsFailure(w, "change credit", err)
	}
}

// creditsFailure keeps the detail of a database error in the log.
func creditsFailure(w http.ResponseWriter, what string, err error) {
	log.Printf("ERROR: credits: %s: %v", what, err)
	httpError(w, errCreditsUnavailable, http.StatusInternalServerError)
}
