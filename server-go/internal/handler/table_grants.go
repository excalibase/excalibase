// Package handler — table / function exposure grants (EXC-370).
//
// Mounted at /api/provision/{projectId}/table-grants. The project sub-router
// already validates auth + project membership and the Developer role gate,
// exactly as it does for /rls-policies and /column-policies; this handler
// treats the path's projectId as authoritative and never trusts the body's.
//
// excalibase-graphql reads GET /table-grants/ alongside the RLS and column
// policies it caches. The response always carries an explicit "enforced"
// flag rather than letting the engine infer one from an empty array:
// {"enforced":true,"grants":[]} means deny everything.
//
// Since EXC-400 that flag is true for every project. The per-project opt-in
// is gone — it defaulted to off, provisioning never turned it on, so granting
// a table changed nothing — and the only remaining switch is the
// installation-wide config.AppConfig.ExposureEnforced, which this handler
// reads at construction and no route can write.
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

const routeGrantID = "/{grantId}"

// validGrantIdentifier matches one unquoted SQL identifier: letters, digits,
// underscore, starting with a letter or underscore. Resources are validated
// part-by-part against it so nothing but plain identifiers can be stored.
var validGrantIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

// TableGrantHandler exposes read + CRUD over the project's exposure list.
//
// enforced is the platform-wide kill switch, copied in at construction. It is
// a field and not a store lookup precisely so that nothing in the request path
// can reach it: there is no code here that writes it, per project or at all.
type TableGrantHandler struct {
	store     storage.TableGrantStore
	enforced  bool
	publisher PolicyChangePublisher // optional
}

// NewTableGrantHandler builds the handler. enforced comes from
// config.AppConfig.ExposureEnforced and applies to every project alike.
func NewTableGrantHandler(store storage.TableGrantStore, enforced bool) *TableGrantHandler {
	return &TableGrantHandler{store: store, enforced: enforced}
}

// SetPublisher wires the NATS publisher post-construction so handler
// construction doesn't depend on NATS being up.
func (h *TableGrantHandler) SetPublisher(p PolicyChangePublisher) { h.publisher = p }

// Routes registers the table-grants sub-tree under the project router.
func (h *TableGrantHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Create)
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

// -------------------- helpers --------------------

// grantSet reads the project's grants and pairs them with the platform's
// enforcement decision. Grants is normalised to a non-nil slice so the flag,
// not the array, carries the meaning.
func (h *TableGrantHandler) grantSet(ctx context.Context, projectID string) (*domain.TableGrantSet, error) {
	grants, err := h.store.ListGrants(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if grants == nil {
		grants = []domain.TableGrant{}
	}
	return &domain.TableGrantSet{ProjectID: projectID, Enforced: h.enforced, Grants: grants}, nil
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

// errGrantRole names the only two roles an exposure grant may target and says
// where anything else belongs, so an operator reaching for "admin" or a
// tenant-specific role is pointed at RLS instead of at a wider grant.
var errGrantRole = errors.New(`role must be "` + domain.GrantRoleAnon + `" or "` +
	domain.GrantRoleAuthenticated + `"; exposure decides what end users can reach, ` +
	`and any other role belongs in an RLS policy`)

// validateGrantRole accepts exactly anon or authenticated. It is deliberately
// an exact match: no trimming, no case folding, no "*". A grant the operator
// cannot read literally off the row is a grant they cannot audit.
func validateGrantRole(role string) error {
	if role == domain.GrantRoleAnon || role == domain.GrantRoleAuthenticated {
		return nil
	}
	return errGrantRole
}
