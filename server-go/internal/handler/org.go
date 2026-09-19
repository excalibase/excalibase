package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	routeUserID          = "/{userId}"
	errInvalidRequest    = "invalid request"
	errOrgNotFound       = "org not found"
	errInsufficientPerms = "insufficient permissions"
)

type OrgHandler struct {
	orgStore      storage.OrgStore
	userStore     storage.UserStore
	instanceStore storage.InstanceStore // optional; used to confirm a project belongs to the URL org
}

func NewOrgHandler(orgStore storage.OrgStore, userStore storage.UserStore) *OrgHandler {
	return &OrgHandler{orgStore: orgStore, userStore: userStore}
}

// SetInstanceStore wires the instance store so the project-member endpoints
// can confirm the URL's projectId actually belongs to the URL's orgId before
// operating on it. Without this, a member of org A could enumerate/manage the
// project members of a project owned by org B simply by putting B's projectId
// in the path. nil leaves the (legacy) behaviour where the org<->project link
// is not enforced — production always wires it.
func (h *OrgHandler) SetInstanceStore(s storage.InstanceStore) {
	h.instanceStore = s
}

// projectBelongsToOrg reports whether projectID is owned by orgID. Returns
// false when the instance store is wired and either the project is unknown or
// belongs to a different org. When the instance store is NOT wired it returns
// true (link not enforced) so self-hosted/legacy deployments keep working.
func (h *OrgHandler) projectBelongsToOrg(projectID, orgID string) bool {
	if h.instanceStore == nil {
		return true
	}
	inst, err := h.instanceStore.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return false
	}
	return inst.OrgID == orgID
}

// Routes wires all org endpoints. The isCloud flag gates the cloud-only
// multi-org lifecycle endpoints (create/delete) so self-hosted deployments
// can't spin up additional orgs beyond the bootstrapped default. Team /
// member / project management endpoints are available in both modes —
// self-hosted users still need to invite teammates to the default org.
func (h *OrgHandler) Routes(r chi.Router, isCloud bool) {
	r.Get("/", h.ListMyOrgs)
	if isCloud {
		// Cloud-only: multi-org creation. Self-hosted has one default org.
		r.Post("/", h.CreateOrg)
	}
	r.Route("/{orgId}", func(r chi.Router) {
		r.Get("/", h.GetOrg)
		r.Patch("/", h.UpdateOrg)
		if isCloud {
			// Cloud-only: deleting orgs. Default org is permanent in self-hosted.
			r.Delete("/", h.DeleteOrg)
		}

		// Team/member management — available in both modes.
		r.Route("/members", func(r chi.Router) {
			r.Get("/", h.ListOrgMembers)
			r.Post("/", h.InviteOrgMember)
			r.Patch(routeUserID, h.UpdateOrgMemberRole)
			r.Delete(routeUserID, h.RemoveOrgMember)
		})

		r.Get("/invites", h.ListPendingInvites)

		r.Route("/projects/{projectId}/members", func(r chi.Router) {
			r.Get("/", h.ListProjectMembers)
			r.Post("/", h.AddProjectMember)
			r.Patch(routeUserID, h.UpdateProjectMemberRole)
			r.Delete(routeUserID, h.RemoveProjectMember)
		})
	})
}

func (h *OrgHandler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidRequest, http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Slug == "" {
		httpError(w, "name and slug are required", http.StatusBadRequest)
		return
	}
	if !isValidSlug(req.Slug) {
		httpError(w, "slug must be 2-50 lowercase alphanumeric characters or hyphens", http.StatusBadRequest)
		return
	}

	org := &domain.Org{
		ID:      uuid.New().String(),
		Name:    req.Name,
		Slug:    req.Slug,
		Tier:    domain.Free,
		OwnerID: user.ID,
	}

	if err := h.orgStore.CreateOrg(r.Context(), org); err != nil {
		httpError(w, "failed to create org", http.StatusInternalServerError)
		return
	}

	// Add creator as owner
	h.orgStore.AddOrgMember(r.Context(), &domain.OrgMember{
		OrgID:  org.ID,
		UserID: user.ID,
		Role:   domain.OrgRoleOwner,
	})

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, org)
}

func (h *OrgHandler) ListMyOrgs(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	var orgs []*domain.Org
	var err error

	// Platform admins see all orgs
	if auth.HasPermission(user.Role, auth.PermViewAny) {
		orgs, err = h.orgStore.FindAllOrgs(r.Context())
	} else {
		orgs, err = h.orgStore.FindOrgsByUser(r.Context(), user.ID)
	}

	if err != nil {
		httpError(w, "failed to list orgs", http.StatusInternalServerError)
		return
	}
	if orgs == nil {
		orgs = []*domain.Org{}
	}
	writeJSON(w, orgs)
}

func (h *OrgHandler) GetOrg(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")

	// Must be a member or platform admin
	if !h.isMemberOrPlatformAdmin(r, orgID, user.ID) {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}

	org, err := h.orgStore.FindOrgByID(r.Context(), orgID)
	if err != nil {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}
	writeJSON(w, org)
}

func (h *OrgHandler) UpdateOrg(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")

	if !h.hasOrgPermission(r, orgID, user.ID, auth.OrgPermUpdateOrg) {
		httpError(w, errInsufficientPerms, http.StatusForbidden)
		return
	}

	org, err := h.orgStore.FindOrgByID(r.Context(), orgID)
	if err != nil {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}

	var req struct {
		Name *string          `json:"name,omitempty"`
		Tier *domain.TierType `json:"tier,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidRequest, http.StatusBadRequest)
		return
	}
	if req.Name != nil {
		org.Name = *req.Name
	}
	if req.Tier != nil {
		if !domain.IsValidTier(*req.Tier) {
			httpError(w, "invalid tier: must be FREE, STANDARD, or ENTERPRISE", http.StatusBadRequest)
			return
		}
		org.Tier = *req.Tier
	}

	if err := h.orgStore.UpdateOrg(r.Context(), org); err != nil {
		httpError(w, "failed to update org", http.StatusInternalServerError)
		return
	}
	writeJSON(w, org)
}

func (h *OrgHandler) DeleteOrg(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")

	if !h.hasOrgPermission(r, orgID, user.ID, auth.OrgPermDeleteOrg) {
		httpError(w, "only org owner can delete", http.StatusForbidden)
		return
	}

	if err := h.orgStore.DeleteOrg(r.Context(), orgID); err != nil {
		httpError(w, "failed to delete org", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// --- Org Members ---

func (h *OrgHandler) ListOrgMembers(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")
	if !h.isMemberOrPlatformAdmin(r, orgID, user.ID) {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}

	members, err := h.orgStore.ListOrgMembers(r.Context(), orgID)
	if err != nil {
		httpError(w, "failed to list members", http.StatusInternalServerError)
		return
	}
	if members == nil {
		members = []*domain.OrgMember{}
	}
	writeJSON(w, members)
}

func (h *OrgHandler) InviteOrgMember(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")

	if !h.hasOrgPermission(r, orgID, user.ID, auth.OrgPermManageMembers) {
		httpError(w, errInsufficientPerms, http.StatusForbidden)
		return
	}

	var req struct {
		UserID string `json:"userId"`
		Email  string `json:"email"`
		Role   string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidRequest, http.StatusBadRequest)
		return
	}
	if req.Role == "" {
		httpError(w, "role is required", http.StatusBadRequest)
		return
	}
	if !isValidOrgRole(req.Role) {
		httpError(w, "invalid role: must be admin, developer, or viewer", http.StatusBadRequest)
		return
	}
	if req.UserID == "" && req.Email == "" {
		httpError(w, "userId or email is required", http.StatusBadRequest)
		return
	}

	h.resolveAndAddMember(w, r, orgID, req.UserID, req.Email, req.Role)
}

// resolveInvitee finds the user an invite names, by id or by email.
func (h *OrgHandler) resolveInvitee(ctx context.Context, userID, email string) (*domain.User, error) {
	if userID != "" {
		return h.userStore.FindUserByID(ctx, userID)
	}
	if email == "" {
		return nil, nil
	}
	return h.userStore.FindUserByEmail(ctx, email)
}

// isValidOrgRole checks if the role is one of the allowed org-level roles.
func isValidOrgRole(role string) bool {
	switch role {
	case domain.OrgRoleAdmin, domain.OrgRoleDeveloper, domain.OrgRoleViewer:
		return true
	}
	return false
}

// resolveAndAddMember looks up the user by email if needed, then either adds
// them as a member directly (user exists) or creates a pending invite.
func (h *OrgHandler) resolveAndAddMember(w http.ResponseWriter, r *http.Request, orgID, userID, email, role string) {
	if h.userStore != nil {
		if u, _ := h.resolveInvitee(r.Context(), userID, email); u != nil {
			if u.IsService() {
				// A service principal is platform-scoped: it authenticates
				// with a capability token, never through an org membership.
				httpError(w, "service accounts cannot be organization members", http.StatusBadRequest)
				return
			}
			userID = u.ID
		}
	}

	if userID != "" {
		if err := h.orgStore.AddOrgMember(r.Context(), &domain.OrgMember{
			OrgID: orgID, UserID: userID, Role: role,
		}); err != nil {
			httpError(w, "failed to add member", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]string{"status": "invited"})
		return
	}
	// User doesn't exist — create pending invite
	inviter := auth.GetUser(r.Context())
	if err := h.orgStore.CreatePendingInvite(r.Context(), &domain.PendingInvite{
		OrgID: orgID, Email: email, Role: role, InvitedBy: inviter.ID,
	}); err != nil {
		httpError(w, "failed to create invite", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]string{"status": "pending", "message": "invite created, user will be added on registration"})
}

func (h *OrgHandler) UpdateOrgMemberRole(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")

	if !h.hasOrgPermission(r, orgID, user.ID, auth.OrgPermManageMembers) {
		httpError(w, errInsufficientPerms, http.StatusForbidden)
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidRequest, http.StatusBadRequest)
		return
	}

	// Validate role
	validOrgRoles := map[string]bool{
		domain.OrgRoleAdmin: true, domain.OrgRoleDeveloper: true, domain.OrgRoleViewer: true,
	}
	if !validOrgRoles[req.Role] {
		httpError(w, "invalid role: must be admin, developer, or viewer", http.StatusBadRequest)
		return
	}

	userID := chi.URLParam(r, "userId")
	if err := h.orgStore.UpdateOrgMemberRole(r.Context(), orgID, userID, req.Role); err != nil {
		httpError(w, "failed to update role", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (h *OrgHandler) RemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")

	if !h.hasOrgPermission(r, orgID, user.ID, auth.OrgPermManageMembers) {
		httpError(w, errInsufficientPerms, http.StatusForbidden)
		return
	}

	userID := chi.URLParam(r, "userId")
	if err := h.orgStore.RemoveOrgMember(r.Context(), orgID, userID); err != nil {
		httpError(w, "failed to remove member", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "removed"})
}

// --- Project Members ---

func (h *OrgHandler) ListProjectMembers(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")
	if !h.isMemberOrPlatformAdmin(r, orgID, user.ID) {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if !h.projectBelongsToOrg(projectID, orgID) {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}

	members, err := h.orgStore.ListProjectMembers(r.Context(), projectID)
	if err != nil {
		httpError(w, "failed to list members", http.StatusInternalServerError)
		return
	}
	if members == nil {
		members = []*domain.ProjectMember{}
	}
	writeJSON(w, members)
}

func (h *OrgHandler) AddProjectMember(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")

	if !h.hasOrgPermission(r, orgID, user.ID, auth.OrgPermManageMembers) {
		httpError(w, errInsufficientPerms, http.StatusForbidden)
		return
	}

	projectID := chi.URLParam(r, "projectId")
	if !h.projectBelongsToOrg(projectID, orgID) {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}

	var req struct {
		UserID string `json:"userId"`
		Role   string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidRequest, http.StatusBadRequest)
		return
	}

	// Validate project role
	validProjectRoles := map[string]bool{
		domain.ProjectRoleAdmin: true, domain.ProjectRoleEditor: true, domain.ProjectRoleViewer: true,
	}
	if !validProjectRoles[req.Role] {
		httpError(w, "invalid role: must be admin, editor, or viewer", http.StatusBadRequest)
		return
	}

	if err := h.orgStore.AddProjectMember(r.Context(), &domain.ProjectMember{
		ProjectID: projectID, OrgID: orgID, UserID: req.UserID, Role: req.Role,
	}); err != nil {
		httpError(w, "failed to add member", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]string{"status": "added"})
}

func (h *OrgHandler) UpdateProjectMemberRole(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")

	if !h.hasOrgPermission(r, orgID, user.ID, auth.OrgPermManageMembers) {
		httpError(w, errInsufficientPerms, http.StatusForbidden)
		return
	}

	projectID := chi.URLParam(r, "projectId")
	if !h.projectBelongsToOrg(projectID, orgID) {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidRequest, http.StatusBadRequest)
		return
	}

	userID := chi.URLParam(r, "userId")
	if err := h.orgStore.UpdateProjectMemberRole(r.Context(), projectID, userID, req.Role); err != nil {
		httpError(w, "failed to update role", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "updated"})
}

func (h *OrgHandler) RemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")

	if !h.hasOrgPermission(r, orgID, user.ID, auth.OrgPermManageMembers) {
		httpError(w, errInsufficientPerms, http.StatusForbidden)
		return
	}

	projectID := chi.URLParam(r, "projectId")
	if !h.projectBelongsToOrg(projectID, orgID) {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}
	userID := chi.URLParam(r, "userId")
	if err := h.orgStore.RemoveProjectMember(r.Context(), projectID, userID); err != nil {
		httpError(w, "failed to remove member", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "removed"})
}

// --- Pending Invites ---

func (h *OrgHandler) ListPendingInvites(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")
	if !h.isMemberOrPlatformAdmin(r, orgID, user.ID) {
		httpError(w, errOrgNotFound, http.StatusNotFound)
		return
	}

	invites, err := h.orgStore.ListPendingInvites(r.Context(), orgID)
	if err != nil {
		httpError(w, "failed to list invites", http.StatusInternalServerError)
		return
	}
	if invites == nil {
		invites = []*domain.PendingInvite{}
	}
	writeJSON(w, invites)
}

// --- Helpers ---

func (h *OrgHandler) isMemberOrPlatformAdmin(r *http.Request, orgID, userID string) bool {
	user := auth.GetUser(r.Context())
	if user != nil && auth.HasPermission(user.Role, auth.PermManageUsers) {
		return true
	}
	_, err := h.orgStore.GetOrgMember(r.Context(), orgID, userID)
	return err == nil
}

func (h *OrgHandler) hasOrgPermission(r *http.Request, orgID, userID string, perm auth.OrgPermission) bool {
	// Platform admins can do anything
	user := auth.GetUser(r.Context())
	if user != nil && auth.HasPermission(user.Role, auth.PermManageUsers) {
		return true
	}

	member, err := h.orgStore.GetOrgMember(r.Context(), orgID, userID)
	if err != nil {
		return false
	}
	return auth.HasOrgPermission(member.Role, perm)
}
