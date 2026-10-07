package handler

import (
	"log"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

var (
	functionCORSMethods = "GET, POST, PUT, DELETE, PATCH, OPTIONS"
	// What the SDK and common clients send: the publishable key and the role
	// header ride every SDK call.
	functionCORSHeaders = strings.Join([]string{
		"Authorization", "Content-Type", "X-Excalibase-Publishable-Key", "X-Excalibase-Role", "apikey", "x-client-info",
	}, ", ")
)

// SetCorsStore wires the per-project browser-origin allowlist, the one the
// project's GraphQL and REST answer to, for its public functions.
func (h *FunctionHandler) SetCorsStore(s storage.ProjectCorsStore) {
	h.corsStore = s
}

// answerCORS sets the CORS headers a project's allowlist grants the request's
// origin and answers a preflight itself, reporting whether it did. The rule is
// the engine's and auth's (EXC-563): without a store, an unreadable one or an
// unlisted origin the browser gets no grant and a preflight is a 403; an actual
// request is still served, because functions take a bearer token, never a
// cookie, and the browser withholds an ungranted response from the page.
func (h *FunctionHandler) answerCORS(w http.ResponseWriter, r *http.Request, projectID string) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		w.Header().Add("Vary", "Origin")
	}
	allowed := h.corsGrant(r, projectID)
	if allowed != "" {
		w.Header().Set("Access-Control-Allow-Origin", allowed)
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", functionCORSMethods)
			w.Header().Set("Access-Control-Allow-Headers", functionCORSHeaders)
			w.Header().Set("Access-Control-Max-Age", "3600")
		}
	}
	if r.Method != http.MethodOptions {
		return false
	}
	if origin != "" && allowed == "" {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

// corsGrant is the Access-Control-Allow-Origin value for the request, or "".
func (h *FunctionHandler) corsGrant(r *http.Request, projectID string) string {
	origin := r.Header.Get("Origin")
	if origin == "" || h.corsStore == nil || !isValidID(projectID) {
		return ""
	}
	listed, err := h.corsStore.GetCorsOrigins(r.Context(), projectID)
	if err != nil {
		log.Printf("WARN: functions CORS allowlist for %s unreadable: %v", projectID, err)
		return ""
	}
	if domain.IsCorsWildcard(listed) {
		return "*"
	}
	canonical, err := domain.ParseCorsOrigins([]string{origin}, false)
	if err != nil || len(canonical) != 1 {
		return ""
	}
	for _, entry := range listed {
		if entry == canonical[0] {
			return origin
		}
	}
	return ""
}
