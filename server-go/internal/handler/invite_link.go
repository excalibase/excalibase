package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// inviteTTL is how long an invite link stays usable.
const inviteTTL = 7 * 24 * time.Hour

// invitePath is the Studio route an invite link opens. The server hands back
// the path only: Studio prefixes its own origin, which the API cannot know.
const invitePath = "/register"

func inviteLink(token string) string {
	return invitePath + "?" + url.Values{"invite": {token}}.Encode()
}

func inviteErrorStatus(err error) int {
	switch {
	case errors.Is(err, storage.ErrInviteInvalid):
		return http.StatusBadRequest
	case errors.Is(err, storage.ErrAlreadyOrgMember):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func inviteErrorMessage(err error) string {
	switch {
	case errors.Is(err, storage.ErrInviteInvalid):
		return storage.ErrInviteInvalid.Error()
	case errors.Is(err, storage.ErrAlreadyOrgMember):
		return storage.ErrAlreadyOrgMember.Error()
	default:
		log.Printf("ERROR: invite: %v", err)
		return "failed to accept invite"
	}
}

// createInviteLink files a pending invite and answers with its one-time link.
// Only the token's hash is stored, so the link cannot be shown again; inviting
// the same address again issues a fresh one.
func (h *OrgHandler) createInviteLink(w http.ResponseWriter, r *http.Request, orgID, email, role string) {
	token, hash, err := mintToken()
	if err != nil {
		httpError(w, "failed to create invite", http.StatusInternalServerError)
		return
	}
	expires := time.Now().Add(inviteTTL)
	inviter := auth.GetUser(r.Context())
	if err := h.orgStore.CreatePendingInvite(r.Context(), &domain.PendingInvite{
		OrgID: orgID, Email: email, Role: role, InvitedBy: inviter.ID,
		TokenHash: hash, ExpiresAt: &domain.FlexTime{Time: expires},
	}); err != nil {
		httpError(w, "failed to create invite", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{
		"status":     "pending",
		"inviteLink": inviteLink(token),
		"expiresAt":  expires.UTC(),
	})
}

// AcceptInvite joins the signed-in caller to the org an invite token names.
// The token is the credential; it is spent on success.
func (h *OrgHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		httpError(w, "token required", http.StatusBadRequest)
		return
	}
	inv, err := h.orgStore.AcceptPendingInvite(r.Context(), hashToken(body.Token), user.ID, time.Now())
	if err != nil {
		httpError(w, inviteErrorMessage(err), inviteErrorStatus(err))
		return
	}
	writeJSON(w, map[string]string{"orgId": inv.OrgID, "role": inv.Role})
}
