package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/endusers"
)

// One end user's details, metadata and deletion (EXC-662 step 4). The caller
// mounts these behind the authuser flag, so they are unmounted while it is off.

const (
	auditActionEndUserUpdate = "end_user.update"
	auditActionEndUserDelete = "end_user.delete"

	// Auth holds each metadata object to 16 KiB; this leaves room for both and a name.
	maxEndUserPatchBodySize = 40 << 10
)

var (
	errUserPatchBody     = errors.New(`body must be {"appMetadata"?: object, "userMetadata"?: object, "fullName"?: string}`)
	errUserPatchNoChange = errors.New("the body changes nothing: give appMetadata, userMetadata or fullName")
)

// AccountRoutes mounts GET, PATCH and DELETE /{userId}.
func (h *EndUsersHandler) AccountRoutes(r chi.Router) {
	r.Get("/{userId}", h.Get)
	r.Patch("/{userId}", h.Update)
	r.Delete("/{userId}", h.Delete)
}

func (h *EndUsersHandler) Get(w http.ResponseWriter, r *http.Request) {
	target, userID, ok := h.resolveUser(w, r)
	if !ok {
		return
	}
	user, err := h.users.Get(r.Context(), target.orgSlug, target.projectID, target.actor, userID)
	if err != nil {
		writeEndUserError(w, err)
		return
	}
	writeRawEndUser(w, user)
}

func (h *EndUsersHandler) Update(w http.ResponseWriter, r *http.Request) {
	target, userID, ok := h.resolveUser(w, r)
	if !ok {
		return
	}
	patch, fields, err := parseUserPatch(w, r)
	if err != nil {
		h.record(r, target, auditActionEndUserUpdate, map[string]any{"userId": userID, "outcome": outcomeInvalidRequest})
		writeCodedError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	user, err := h.users.Update(r.Context(), target.orgSlug, target.projectID, target.actor, userID, patch)
	h.record(r, target, auditActionEndUserUpdate, withOutcome(map[string]any{"userId": userID, "fields": fields}, err))
	if err != nil {
		writeEndUserError(w, err)
		return
	}
	writeRawEndUser(w, user)
}

func (h *EndUsersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	target, userID, ok := h.resolveUser(w, r)
	if !ok {
		return
	}
	err := h.users.Delete(r.Context(), target.orgSlug, target.projectID, target.actor, userID)
	h.record(r, target, auditActionEndUserDelete, withOutcome(map[string]any{"userId": userID}, err))
	if err != nil {
		writeEndUserError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveUser resolves the project and the {userId} the request names.
func (h *EndUsersHandler) resolveUser(w http.ResponseWriter, r *http.Request) (endUserTarget, int64, bool) {
	target, ok := h.resolve(w, r)
	if !ok {
		return endUserTarget{}, 0, false
	}
	userID, err := strconv.ParseInt(chi.URLParam(r, "userId"), 10, 64)
	if err != nil || userID <= 0 {
		writeCodedError(w, http.StatusBadRequest, codeInvalidRequest, errRoleChangeUserID.Error())
		return endUserTarget{}, 0, false
	}
	return target, userID, true
}

// parseUserPatch accepts only the fields an admin may change and names the
// ones given, for the audit log; their values never leave this request.
func parseUserPatch(w http.ResponseWriter, r *http.Request) (endusers.UserPatch, []string, error) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEndUserPatchBodySize))
	decoder.DisallowUnknownFields()
	var patch endusers.UserPatch
	if err := decoder.Decode(&patch); err != nil {
		return endusers.UserPatch{}, nil, errUserPatchBody
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return endusers.UserPatch{}, nil, errUserPatchBody
	}
	var fields []string
	for _, field := range []struct {
		name string
		raw  json.RawMessage
	}{{"appMetadata", patch.AppMetadata}, {"userMetadata", patch.UserMetadata}} {
		if field.raw == nil {
			continue
		}
		if !isJSONObject(field.raw) {
			return endusers.UserPatch{}, nil, errUserPatchBody
		}
		fields = append(fields, field.name)
	}
	if patch.FullName != nil {
		fields = append(fields, "fullName")
	}
	if len(fields) == 0 {
		return endusers.UserPatch{}, nil, errUserPatchNoChange
	}
	return patch, fields, nil
}

// isJSONObject reads well-formed JSON only, as the decoder has already checked it.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func writeRawEndUser(w http.ResponseWriter, user json.RawMessage) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(user)
}
