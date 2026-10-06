package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// maxProvisionBodyBytes bounds a create-project or add-database body.
const maxProvisionBodyBytes = 64 << 10

// provisioningBody is a create-project or add-database body: the request,
// plus the tier a caller may state. The organisation sets the tier, so a
// stated one is only checked against it, never applied.
type provisioningBody struct {
	domain.ProvisioningRequest
	Tier *string `json:"tier"`
}

// decodeProvisioningRequest decodes a create-project or add-database body,
// refusing by name any field the platform does not read, so a caller never
// believes a setting took effect when it was ignored (EXC-555). It returns
// the tier the body stated, nil when it stated none.
func decodeProvisioningRequest(w http.ResponseWriter, r *http.Request, into *domain.ProvisioningRequest) (*string, bool) {
	var body provisioningBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProvisionBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			httpError(w, fmt.Sprintf("request body is larger than %d bytes", tooLarge.Limit), http.StatusRequestEntityTooLarge)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			httpError(w, strings.TrimPrefix(err.Error(), "json: ")+" is not a provisioning setting", http.StatusBadRequest)
		default:
			httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		}
		return nil, false
	}
	*into = body.ProvisioningRequest
	if named := unimplementedProvisioningFields(*into); len(named) > 0 {
		httpError(w, "not supported yet: "+strings.Join(named, ", "), http.StatusBadRequest)
		return nil, false
	}
	return body.Tier, true
}

// confirmStatedTier answers 400 when a body stated a tier that is not the
// plan of orgID. It runs after the caller's access to orgID is settled, as
// the refusal names that organisation's plan.
func (h *ProvisioningHandler) confirmStatedTier(w http.ResponseWriter, r *http.Request, orgID string, stated *string) bool {
	if stated == nil {
		return true
	}
	err := h.svc.ConfirmStatedTier(r.Context(), orgID, *stated)
	switch {
	case err == nil:
		return true
	case errors.Is(err, service.ErrTierNotOrgPlan):
		httpError(w, err.Error(), http.StatusBadRequest)
	case !writeProjectCreationError(w, err):
		httpError(w, safeError(err), http.StatusBadRequest)
	}
	return false
}

// unimplementedProvisioningFields names the request fields the platform
// accepts in its schema but does not act on.
func unimplementedProvisioningFields(req domain.ProvisioningRequest) []string {
	var named []string
	if req.Network != nil {
		named = append(named, "network")
	}
	if req.Maintenance != nil {
		named = append(named, "maintenance")
	}
	if req.Pooler != nil {
		named = append(named, "pooler")
	}
	if req.WebhookURL != "" {
		named = append(named, "webhookUrl")
	}
	if req.ParameterGroup != "" {
		named = append(named, "parameterGroupName")
	}
	return named
}
