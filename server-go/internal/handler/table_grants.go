// Package handler — table / function exposure grants (EXC-370).
//
// Mounted at /api/provision/{projectId}/table-grants. The project sub-router
// already validates auth + project membership and the Developer role gate,
// exactly as it does for /rls-policies and /column-policies; this handler
// treats the path's projectId as authoritative and never trusts the body's.
//
// excalibase-graphql reads GET /table-grants/ alongside the RLS and column
// policies it caches. The response always carries an explicit "enforced"
// flag: an empty grants array on its own cannot distinguish a project that
// never configured exposure (do not enforce) from one that is enforced with
// nothing granted (deny everything), and guessing would take every existing
// tenant offline on upgrade.
//
// After any successful write the NATS publisher (injected via SetPublisher)
// emits "policies.{projectId}.changed" — the same subject the policy writes
// use — so the engine evicts cached policies and grants together.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	routeGrantID     = "/{grantId}"
	routeEnforcement = "/enforcement"
)

// validGrantIdentifier matches one unquoted SQL identifier: letters, digits,
// underscore, starting with a letter or underscore. Resources are validated
// part-by-part against it so nothing but plain identifiers can be stored.
var validGrantIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

// TableGrantHandler exposes read + CRUD over the per-project exposure list
// and the project's enforcement flag.
type TableGrantHandler struct {
	store     storage.TableGrantStore
	publisher PolicyChangePublisher // optional
}

func NewTableGrantHandler(store storage.TableGrantStore) *TableGrantHandler {
	return &TableGrantHandler{store: store}
}

// SetPublisher wires the NATS publisher post-construction so handler
// construction doesn't depend on NATS being up.
func (h *TableGrantHandler) SetPublisher(p PolicyChangePublisher) { h.publisher = p }

// Routes registers the table-grants sub-tree under the project router.
func (h *TableGrantHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Put(routeEnforcement, h.SetEnforcement)
	r.Patch(routeGrantID, h.Update)
	r.Delete(routeGrantID, h.Delete)
}

// List returns the project's grants together with the explicit enforcement
// flag. This is the endpoint excalibase-graphql polls.
func (h *TableGrantHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	set, err := h.grantSet(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, set)
}

func (h *TableGrantHandler) Create(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	var g domain.TableGrant
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	g.ProjectID = projectID
	if g.ID == "" {
		g.ID = uuid.NewString()
	}
	if err := validateGrant(&g); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertGrant(r.Context(), &g); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	h.publish(r.Context(), domain.PolicyChangeEvent{
		ProjectID: projectID, Kind: domain.GrantChangeKind, Resource: g.Resource,
		PolicyID: g.ID, Op: "create",
	})
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, &g)
}

// Update applies a partial change onto the stored grant: the body is decoded
// over the existing record, so omitted fields keep their current values.
func (h *TableGrantHandler) Update(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "grantId")

	existing, err := h.store.GetGrant(r.Context(), projectID, id)
	if errors.Is(err, pgstore.ErrGrantNotFound) {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}

	if err := json.NewDecoder(r.Body).Decode(existing); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	existing.ID = id
	existing.ProjectID = projectID
	if err := validateGrant(existing); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertGrant(r.Context(), existing); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	h.publish(r.Context(), domain.PolicyChangeEvent{
		ProjectID: projectID, Kind: domain.GrantChangeKind, Resource: existing.Resource,
		PolicyID: id, Op: "update",
	})
	writeJSON(w, existing)
}

func (h *TableGrantHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "grantId")
	// Capture the resource for the change event before the row disappears.
	existing, _ := h.store.GetGrant(r.Context(), projectID, id)
	if err := h.store.DeleteGrant(r.Context(), projectID, id); err != nil {
		if errors.Is(err, pgstore.ErrGrantNotFound) {
			httpError(w, errNotFound, http.StatusNotFound)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	resource := ""
	if existing != nil {
		resource = existing.Resource
	}
	h.publish(r.Context(), domain.PolicyChangeEvent{
		ProjectID: projectID, Kind: domain.GrantChangeKind, Resource: resource,
		PolicyID: id, Op: "delete",
	})
	w.WriteHeader(http.StatusNoContent)
}

// enforcementRequest is the toggle body. The pointer distinguishes "field
// omitted" from an explicit false, so a malformed body can't silently
// disable enforcement for a project.
type enforcementRequest struct {
	Enforced *bool `json:"enforced"`
}

// SetEnforcement turns exposure enforcement on or off for the project and
// answers with the full grant set so Studio sees the resulting state.
func (h *TableGrantHandler) SetEnforcement(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	var req enforcementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	if req.Enforced == nil {
		httpError(w, "enforced is required (true or false)", http.StatusBadRequest)
		return
	}
	if err := h.store.SetExposureEnforced(r.Context(), projectID, *req.Enforced); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	h.publish(r.Context(), domain.PolicyChangeEvent{
		ProjectID: projectID, Kind: domain.ExposureChangeKind, Op: "update",
	})

	set, err := h.grantSet(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, set)
}

// -------------------- helpers --------------------

// grantSet reads both halves of the exposure state. Grants is normalised to a
// non-nil slice so the flag, not the array, carries the meaning.
func (h *TableGrantHandler) grantSet(ctx context.Context, projectID string) (*domain.TableGrantSet, error) {
	enforced, err := h.store.IsExposureEnforced(ctx, projectID)
	if err != nil {
		return nil, err
	}
	grants, err := h.store.ListGrants(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if grants == nil {
		grants = []domain.TableGrant{}
	}
	return &domain.TableGrantSet{ProjectID: projectID, Enforced: enforced, Grants: grants}, nil
}

func (h *TableGrantHandler) publish(ctx context.Context, evt domain.PolicyChangeEvent) {
	if h.publisher == nil {
		return
	}
	h.publisher.PublishPolicyChange(ctx, evt)
}

func validateGrant(g *domain.TableGrant) error {
	if err := validateGrantResource(g.Resource); err != nil {
		return err
	}
	if len(g.Operations) == 0 {
		return errors.New("at least one operation required")
	}
	for _, op := range g.Operations {
		switch op {
		case domain.OpSelect, domain.OpInsert, domain.OpUpdate, domain.OpDelete:
		default:
			return errors.New("unknown operation: " + string(op))
		}
	}
	return validateGrantRole(g.Role)
}

// validateGrantResource accepts "table" or "schema.table" (equally, a
// function), each part a plain SQL identifier. Anything quoted, spaced or
// deeper is rejected before it reaches storage.
func validateGrantResource(resource string) error {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return errors.New("resource required (table or function name)")
	}
	parts := strings.Split(resource, ".")
	if len(parts) > 2 {
		return errors.New("invalid resource (expected name or schema.name)")
	}
	for _, part := range parts {
		if !validGrantIdentifier.MatchString(part) {
			return errors.New("invalid resource (expected name or schema.name)")
		}
	}
	return nil
}

// validateGrantRole accepts "*" (every role) or one plain identifier.
func validateGrantRole(role string) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return errors.New("role required")
	}
	if role != "*" && !validGrantIdentifier.MatchString(role) {
		return errors.New("invalid role")
	}
	return nil
}
