// Package handler — RLS / CLS policy CRUD endpoints (EXC-318).
//
// Mounted at /api/provision/{projectId}/rls-policies and
// /api/provision/{projectId}/column-policies. The project sub-router
// already validates auth + project ownership; this handler treats the
// path's projectId as authoritative and never trusts the body's value.
//
// Excalibase-graphql calls these endpoints to populate its in-process
// PolicyCache. After any successful write, the NATS publisher (injected
// via SetPublisher) emits "policies.{projectId}.changed" so consumers
// invalidate without polling.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"

	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	routePolicyID  = "/{policyId}"
	errNotFound    = "not found"
	errInvalidJSON = "invalid json"
)

// PolicyChangePublisher is implemented by whoever owns the NATS connection.
// Wired from cmd/server/main.go; nil-tolerant so unit tests that don't
// care about events can leave it unset.
type PolicyChangePublisher interface {
	PublishPolicyChange(ctx context.Context, evt domain.PolicyChangeEvent)
}

// RlsPolicyHandler exposes CRUD for both rls_policies and column_policies.
type RlsPolicyHandler struct {
	store     storage.RlsPolicyStore
	publisher PolicyChangePublisher // optional
}

func NewRlsPolicyHandler(store storage.RlsPolicyStore) *RlsPolicyHandler {
	return &RlsPolicyHandler{store: store}
}

// SetPublisher wires the NATS publisher post-construction so handler
// construction doesn't depend on NATS being up.
func (h *RlsPolicyHandler) SetPublisher(p PolicyChangePublisher) { h.publisher = p }

// RlsRoutes registers the rls-policies sub-tree under the project router.
func (h *RlsPolicyHandler) RlsRoutes(r chi.Router) {
	r.Get("/", h.ListRls)
	r.Post("/", h.CreateRls)
	r.Get(routePolicyID, h.GetRls)
	r.Patch(routePolicyID, h.UpdateRls)
	r.Delete(routePolicyID, h.DeleteRls)
}

// ColumnRoutes registers the column-policies sub-tree under the project router.
func (h *RlsPolicyHandler) ColumnRoutes(r chi.Router) {
	r.Get("/", h.ListColumn)
	r.Post("/", h.CreateColumn)
	r.Get(routePolicyID, h.GetColumn)
	r.Patch(routePolicyID, h.UpdateColumn)
	r.Delete(routePolicyID, h.DeleteColumn)
}

// -------------------- RLS handlers --------------------

func (h *RlsPolicyHandler) ListRls(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	resource := r.URL.Query().Get("resource")
	out, err := h.store.ListRls(r.Context(), projectID, resource)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (h *RlsPolicyHandler) GetRls(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "policyId")
	p, err := h.store.GetRls(r.Context(), projectID, id)
	if errors.Is(err, pgstore.ErrPolicyNotFound) {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, p)
}

func (h *RlsPolicyHandler) CreateRls(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	var p domain.Policy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	p.ProjectID = projectID
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if err := validateRls(&p); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertRls(r.Context(), &p); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	h.publish(r.Context(), domain.PolicyChangeEvent{
		ProjectID: projectID, Kind: "rls", Resource: p.Resource,
		PolicyID: p.ID, Op: "create",
	})
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, &p)
}

func (h *RlsPolicyHandler) UpdateRls(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "policyId")

	existing, err := h.store.GetRls(r.Context(), projectID, id)
	if errors.Is(err, pgstore.ErrPolicyNotFound) {
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
	if err := validateRls(existing); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertRls(r.Context(), existing); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	h.publish(r.Context(), domain.PolicyChangeEvent{
		ProjectID: projectID, Kind: "rls", Resource: existing.Resource,
		PolicyID: id, Op: "update",
	})
	writeJSON(w, existing)
}

func (h *RlsPolicyHandler) DeleteRls(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "policyId")
	// Capture resource for the NATS payload before deleting.
	existing, _ := h.store.GetRls(r.Context(), projectID, id)
	if err := h.store.DeleteRls(r.Context(), projectID, id); err != nil {
		if errors.Is(err, pgstore.ErrPolicyNotFound) {
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
		ProjectID: projectID, Kind: "rls", Resource: resource, PolicyID: id, Op: "delete",
	})
	w.WriteHeader(http.StatusNoContent)
}

// -------------------- Column handlers --------------------

func (h *RlsPolicyHandler) ListColumn(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	resource := r.URL.Query().Get("resource")
	out, err := h.store.ListColumn(r.Context(), projectID, resource)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (h *RlsPolicyHandler) GetColumn(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "policyId")
	p, err := h.store.GetColumn(r.Context(), projectID, id)
	if errors.Is(err, pgstore.ErrPolicyNotFound) {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, p)
}

func (h *RlsPolicyHandler) CreateColumn(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	var p domain.ColumnPolicy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	p.ProjectID = projectID
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if err := validateColumn(&p); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertColumn(r.Context(), &p); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	h.publish(r.Context(), domain.PolicyChangeEvent{
		ProjectID: projectID, Kind: "column", Resource: p.Resource,
		PolicyID: p.ID, Op: "create",
	})
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, &p)
}

func (h *RlsPolicyHandler) UpdateColumn(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "policyId")

	existing, err := h.store.GetColumn(r.Context(), projectID, id)
	if errors.Is(err, pgstore.ErrPolicyNotFound) {
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
	if err := validateColumn(existing); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertColumn(r.Context(), existing); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	h.publish(r.Context(), domain.PolicyChangeEvent{
		ProjectID: projectID, Kind: "column", Resource: existing.Resource,
		PolicyID: id, Op: "update",
	})
	writeJSON(w, existing)
}

func (h *RlsPolicyHandler) DeleteColumn(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "policyId")
	existing, _ := h.store.GetColumn(r.Context(), projectID, id)
	if err := h.store.DeleteColumn(r.Context(), projectID, id); err != nil {
		if errors.Is(err, pgstore.ErrPolicyNotFound) {
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
		ProjectID: projectID, Kind: "column", Resource: resource, PolicyID: id, Op: "delete",
	})
	w.WriteHeader(http.StatusNoContent)
}

// -------------------- helpers --------------------

func (h *RlsPolicyHandler) publish(ctx context.Context, evt domain.PolicyChangeEvent) {
	if h.publisher == nil {
		return
	}
	h.publisher.PublishPolicyChange(ctx, evt)
}

// projectIDFromPath returns the validated projectId from the chi URL path,
// or writes a 400 + reports !ok.
func projectIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "projectId")
	if !isValidID(id) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	return id, true
}

func validateRls(p *domain.Policy) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name required")
	}
	if !isValidID(p.Resource) {
		return errors.New("invalid resource (must be a table name)")
	}
	if p.Effect != domain.EffectAllow && p.Effect != domain.EffectDeny {
		return errors.New("effect must be ALLOW or DENY")
	}
	if len(p.Operations) == 0 {
		return errors.New("at least one operation required")
	}
	for _, op := range p.Operations {
		switch op {
		case domain.OpSelect, domain.OpInsert, domain.OpUpdate, domain.OpDelete:
		default:
			return errors.New("unknown operation: " + string(op))
		}
	}
	if p.RuleLogic == "" {
		p.RuleLogic = domain.LogicAnd
	}
	if p.RuleLogic != domain.LogicAnd && p.RuleLogic != domain.LogicOr {
		return errors.New("rule_logic must be AND or OR")
	}
	if len(p.Rules) == 0 {
		return errors.New("at least one rule required")
	}
	if len(p.Assignments) == 0 {
		return errors.New("at least one assignment required")
	}
	return nil
}

func validateColumn(p *domain.ColumnPolicy) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name required")
	}
	if !isValidID(p.Resource) {
		return errors.New("invalid resource (must be a table name)")
	}
	if len(p.Columns) == 0 {
		return errors.New("at least one column required")
	}
	for _, c := range p.Columns {
		if !isValidID(c) {
			return errors.New("invalid column name: " + c)
		}
	}
	if len(p.Operations) == 0 {
		return errors.New("at least one operation required")
	}
	if err := validateColumnMode(p); err != nil {
		return err
	}
	if len(p.Assignments) == 0 {
		return errors.New("at least one assignment required")
	}
	return nil
}

// validateColumnMode enforces the per-mode invariants on a column policy: which
// of partial_spec / custom_masker_key each masking mode requires or forbids.
func validateColumnMode(p *domain.ColumnPolicy) error {
	switch p.Mode {
	case domain.MaskHide, domain.MaskNull:
		if p.PartialSpec != nil {
			return errors.New(string(p.Mode) + " mode does not use partial_spec")
		}
		if p.CustomMaskerKey != "" {
			return errors.New(string(p.Mode) + " mode does not use custom_masker_key")
		}
	case domain.MaskPartial:
		if p.PartialSpec == nil {
			return errors.New("PARTIAL mode requires partial_spec")
		}
	case domain.MaskHash:
		if p.PartialSpec != nil || p.CustomMaskerKey != "" {
			return errors.New("HASH mode does not use partial_spec or custom_masker_key")
		}
	case domain.MaskCustom:
		if strings.TrimSpace(p.CustomMaskerKey) == "" {
			return errors.New("CUSTOM mode requires custom_masker_key")
		}
	default:
		return errors.New("unknown mode: " + string(p.Mode))
	}
	return nil
}
