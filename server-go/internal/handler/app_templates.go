package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/apptemplate"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// AppTemplateAPI is the template service as this handler uses it.
type AppTemplateAPI interface {
	List(ctx context.Context, projectID string, mayChangeNetwork bool) ([]service.TemplateView, error)
	Get(ctx context.Context, projectID, templateID string, mayChangeNetwork bool) (service.TemplateView, error)
	Deploy(ctx context.Context, projectID, templateID, actor string, opts service.TemplateDeployOptions) (*service.TemplateDeployResult, error)
}

// AppTemplateHandler serves /api/projects/{projectId}/app-templates (EXC-526):
// the built-in templates with what each costs this project, and their deploy.
// The route lets developers deploy; opening the project's private network on
// the way needs an admin or owner, decided here from the caller's role.
type AppTemplateHandler struct {
	api AppTemplateAPI
}

func NewAppTemplateHandler(api AppTemplateAPI) *AppTemplateHandler {
	return &AppTemplateHandler{api: api}
}

// Routes mounts reads for every member; the caller wraps deploy with the developer gate.
func (h *AppTemplateHandler) Routes(r chi.Router) {
	h.ReadRoutes(r)
	r.Post("/{templateId}/deploy", h.Deploy)
}

func (h *AppTemplateHandler) ReadRoutes(r chi.Router) {
	r.Get("/", h.List)
	r.Get("/{templateId}", h.Get)
}

const maxTemplateDeployBody = 1024

type templateDeployRequest struct {
	ConfirmPrivateNetwork bool `json:"confirmPrivateNetwork"`
}

func (h *AppTemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	views, err := h.api.List(r.Context(), projectID, mayChangeNetwork(r))
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSON(w, views)
}

func (h *AppTemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID, templateID, ok := templatePath(w, r)
	if !ok {
		return
	}
	view, err := h.api.Get(r.Context(), projectID, templateID, mayChangeNetwork(r))
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSON(w, view)
}

func (h *AppTemplateHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	projectID, templateID, ok := templatePath(w, r)
	if !ok {
		return
	}
	var req templateDeployRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateDeployBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		httpError(w, `body must be {"confirmPrivateNetwork": true|false}`, http.StatusBadRequest)
		return
	}
	result, err := h.api.Deploy(r.Context(), projectID, templateID, actorID(r), service.TemplateDeployOptions{
		ConfirmPrivateNetwork:   req.ConfirmPrivateNetwork,
		MayChangePrivateNetwork: mayChangeNetwork(r),
	})
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, result)
}

// mayChangeNetwork is the rule of PUT .../app-network: an org admin or owner.
func mayChangeNetwork(r *http.Request) bool {
	access := custommw.ProjectAccessFromContext(r.Context())
	return access != nil && access.RoleAtLeast(domain.OrgRoleAdmin)
}

func templatePath(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return "", "", false
	}
	templateID := chi.URLParam(r, "templateId")
	if !apptemplate.ValidID(templateID) {
		httpError(w, "invalid templateId", http.StatusBadRequest)
		return "", "", false
	}
	return projectID, templateID, true
}

func writeTemplateError(w http.ResponseWriter, err error) {
	var refused *service.TemplateRefusedError
	var failed *service.TemplateFailedError
	var incomplete *service.TemplateRollbackError
	switch {
	case errors.Is(err, service.ErrTemplateNotFound):
		httpError(w, "template not found", http.StatusNotFound)
	case errors.Is(err, service.ErrTemplateProjectNotFound):
		httpError(w, "the project has no record to deploy into", http.StatusNotFound)
	case errors.As(err, &refused):
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": refused.Error(), "reasons": refused.Reasons, "status": http.StatusConflict})
	case errors.Is(err, service.ErrTemplateNetworkNeedsAdmin):
		httpError(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, service.ErrTemplateNetworkUnconfirmed):
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "confirm_private_network", "status": http.StatusConflict})
	case errors.As(err, &incomplete):
		log.Printf("template deploy: rollback incomplete: %v", err)
		httpError(w, "the template was not deployed and could not remove "+strings.Join(incomplete.Remaining, ", ")+"; delete them and deploy again", http.StatusInternalServerError)
	case errors.As(err, &failed):
		log.Printf("template deploy: rolled back: %v", err)
		code := http.StatusConflict
		if failed.ServerFault {
			code = http.StatusInternalServerError
		}
		httpError(w, failed.Public(), code)
	case errors.Is(err, service.ErrProjectOperationRunning):
		httpError(w, "another operation is running on this project; try again when it is done", http.StatusConflict)
	default:
		log.Printf("app templates: %v", err)
		httpError(w, "the template request did not complete; try again", http.StatusInternalServerError)
	}
}
