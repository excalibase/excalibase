package handler

import (
	"context"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// customerSafeFailures are stored reasons that are our own sentences and name
// nothing of the cluster. A customer reads these as failureMessage; any other
// stored text is withheld and shown as its kind.
var customerSafeFailures = []string{
	service.ErrPauseBackupNotCompleted.Error(),
	service.ErrPauseNotObserved.Error(),
	service.ErrResumeNotObserved.Error(),
	service.AddInterruptedReason,
}

// customerSafeFailurePrefixes cover reasons with a fixed opening and a backup
// id after it: an in-place restore that stopped names the backup that holds
// the customer's data (service/restore_in_place.go).
var customerSafeFailurePrefixes = []string{
	"the restore did not complete",
}

// publicFailureMessage is text when it is one of our own sentences, else "".
func publicFailureMessage(text string) string {
	for _, sentence := range customerSafeFailures {
		if text == sentence {
			return text
		}
	}
	for _, prefix := range customerSafeFailurePrefixes {
		if strings.HasPrefix(text, prefix) {
			return text
		}
	}
	return ""
}

// failureView is a stored provisioning failure as one caller reads it.
type failureView struct {
	Kind        domain.FailureKind
	Message     string
	Reason      string
	RollbackLog string
}

// failureForCaller is the one place a stored provisioning failure becomes
// what a caller reads (EXC-869). Everyone gets the plain kind and, for our own
// sentences, the message; the raw text and rollback log, which name pods,
// volumes and namespaces, go only to platform admins and operators. Every
// answer that carries them calls this.
func failureForCaller(ctx context.Context, reason, rollbackLog string) failureView {
	view := failureView{Kind: domain.ClassifyFailure(reason), Message: publicFailureMessage(reason)}
	if user := auth.GetUser(ctx); user != nil && auth.SeesRawProvisioningText(user.Role) {
		view.Reason, view.RollbackLog = reason, rollbackLog
	}
	return view
}

// deletionStopped is what a customer reads of a teardown that stopped; the
// cause names buckets and keys, and stays in the log and with platform staff.
const deletionStopped = "the deletion did not complete; delete it again, and if it keeps failing tell us"

// deletionErrorForCaller is a stopped teardown's error as the caller reads it.
func deletionErrorForCaller(ctx context.Context, raw, namespace string) string {
	if raw == "" {
		return ""
	}
	if user := auth.GetUser(ctx); user != nil && auth.SeesRawProvisioningText(user.Role) {
		return domain.ScrubClusterText(raw, namespace)
	}
	return deletionStopped
}
