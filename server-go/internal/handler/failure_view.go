package handler

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// failureForCaller is the one place a stored provisioning failure becomes
// what a caller reads (EXC-869). Everyone gets the plain kind; the raw text
// and rollback log, which name pods, volumes and namespaces, go only to
// platform admins and operators. Every answer that carries them calls this.
func failureForCaller(ctx context.Context, reason, rollbackLog string) (domain.FailureKind, string, string) {
	kind := domain.ClassifyFailure(reason)
	if user := auth.GetUser(ctx); user != nil && auth.SeesRawProvisioningText(user.Role) {
		return kind, reason, rollbackLog
	}
	return kind, "", ""
}
