package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	auditDBPasswordReset = "database.password.reset"
	// maxPasswordBodyBytes bounds a reset body: a password and a little JSON.
	maxPasswordBodyBytes = 4 << 10
)

// SetAuditLog records password resets; nil keeps them unrecorded.
func (h *ProvisioningHandler) SetAuditLog(audit auditWriter) { h.audit = audit }

// auditPasswordReset records that a database password was reset and by whom,
// never the password itself.
func (h *ProvisioningHandler) auditPasswordReset(r *http.Request, projectID string, chosen bool) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{"chosen": chosen})
	now := time.Now()
	entry := &domain.AuditEntry{UserID: actorID(r), Action: auditDBPasswordReset, Resource: "database",
		ResourceID: projectID, ProjectID: projectID, Details: string(details), IPAddress: clientIP(r), Timestamp: &now}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", auditDBPasswordReset, safeLog(projectID), err)
	}
}
