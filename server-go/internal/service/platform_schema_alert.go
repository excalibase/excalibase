package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// errPlatformSchemaLeft is a reconcile that kept every revoke it could and left
// customer-owned objects in a platform schema for an operator (EXC-821).
var errPlatformSchemaLeft = errors.New("a platform schema holds customer objects left for an operator")

// errPlatformRoleSquatted is a registration refused because the owner created a
// role named like one of the platform's. The platform does not reclaim or drop
// such a role: it may own objects and carries attributes, a password and
// memberships the owner chose, so adopting it would hand them the platform's
// name. An operator reads the alert, which names it, and decides.
var errPlatformRoleSquatted = errors.New("a platform role name was taken outside the platform")

// errPlatformRepairFailed is a repair script that failed for a reason of its own.
var errPlatformRepairFailed = errors.New("the platform schema repair failed")

// errPlatformRepairCutOff is a repair script a statement or lock timeout stopped.
var errPlatformRepairCutOff = errors.New("the platform schema repair was cut off by a timeout")

// platformSchemaMetric keys the project alert while a schema is left.
const platformSchemaMetric = "project.platform-schema"

// SetSchemaAlerts raises a project alert, in the alerts table the pod-pressure
// and WAL archive alerts use, when the reconcile leaves a platform schema for
// an operator, and resolves it once the schema is clean.
func (s *ProvisioningService) SetSchemaAlerts(alerts *AlertingService) { s.schemaAlerts = alerts }

// schemaLeftError marks a failed reconcile as a schema left for an operator
// when its text carries the refusal's marker; as a repair cut off when a
// statement or lock timeout, or the reconcile's deadline, stopped it; and as a
// failed repair unless the last script to announce itself provably held no
// repair work. It fails closed: an error with no script named alerts.
func schemaLeftError(err error) error {
	switch {
	case err == nil || isSchemaAlertError(err):
		return err
	case len(squattedRoles(err.Error())) > 0:
		return fmt.Errorf("%w: %v", errPlatformRoleSquatted, err)
	case strings.Contains(err.Error(), platformSchemaLeftMarker):
		return fmt.Errorf("%w: %v", errPlatformSchemaLeft, err)
	case errors.Is(err, context.DeadlineExceeded) || isTimeoutError(err.Error()):
		return fmt.Errorf("%w: %v", errPlatformRepairCutOff, err)
	case failedInRepairScript(err.Error()):
		return fmt.Errorf("%w: %v", errPlatformRepairFailed, err)
	}
	return err
}

// timeoutError matches a statement_timeout (57014) or lock_timeout (55P03) as
// psql reports it, or the server's own words for either, or a context deadline.
var timeoutError = regexp.MustCompile(`57014|55P03|canceling statement due to (statement|lock) timeout|context deadline exceeded`)

func isTimeoutError(text string) bool { return timeoutError.MatchString(text) }

var repairStage = regexp.MustCompile(regexp.QuoteMeta(repairStageMarker) + `(\w+)`)

// failedInRepairScript is true unless the last script that printed its name
// before the failure is one that cannot hold repair work: the password repair
// or the registration SQL, both after the lockdown and before the quarantine. A
// failure with no name at all is true too: the repair may not have run.
func failedInRepairScript(text string) bool {
	stages := repairStage.FindAllStringSubmatch(text, -1)
	if len(stages) == 0 {
		return true
	}
	switch stages[len(stages)-1][1] {
	case "password", "registration":
		return false
	}
	return true
}

// trackSchemaAlert opens the alert while left is true and resolves it when not.
func (s *ProvisioningService) trackSchemaAlert(projectID string, left bool, reason error) {
	if s.schemaAlerts == nil {
		return
	}
	s.schemaAlerts.Track(projectID, platformSchemaMetric, left, func() domain.Alert {
		now := time.Now()
		return domain.Alert{
			ID: fmt.Sprintf("%s-%s-%d", platformSchemaMetric, projectID, now.UnixNano()), Severity: "CRITICAL",
			Message:   schemaAlertMessage(reason),
			Timestamp: &domain.FlexTime{Time: now},
		}
	})
}

func schemaAlertMessage(reason error) string {
	if errors.Is(reason, errPlatformRepairCutOff) {
		return "The repair of the project's platform schemas was cut off by a statement or lock timeout, so it did not finish. " +
			"An operator must find what slows it (OPERATOR.md 6.10)."
	}
	if errors.Is(reason, errPlatformRoleSquatted) {
		return "Registration is refused because role(s) " + strings.Join(squattedRoles(reason.Error()), ", ") +
			" were created outside the platform, so the project's registration SQL cannot run. An operator must review and drop them (OPERATOR.md 6.10)."
	}
	if errors.Is(reason, errPlatformRepairFailed) {
		return "A script of the platform schema repair failed, so customer grants or objects may remain in the project's auth or excalibase_cdc schema. " +
			"An operator must read the reconcile log (OPERATOR.md 6.10)."
	}
	return "A customer role owns objects in the project's auth or excalibase_cdc schema that the platform cannot move or adopt. " +
		"Every grant was revoked; an operator must review them (OPERATOR.md 6.10)."
}

var squatText = regexp.MustCompile(`platform role\(s\) ([^\n]+?) were created outside the platform`)

// squattedRoles are the reserved role names the registration SQL refused on.
// Only names on the platform's own list count, so nothing else in an error text
// reaches an alert.
func squattedRoles(text string) []string {
	match := squatText.FindStringSubmatch(text)
	if match == nil {
		return nil
	}
	var names []string
	for _, name := range strings.Split(match[1], ", ") {
		if pgroles.IsReserved(name) {
			names = append(names, name)
		}
	}
	return names
}

// isSchemaAlertError is true for an error that opens the project alert.
func isSchemaAlertError(err error) bool {
	for _, reason := range []error{errPlatformSchemaLeft, errPlatformRepairCutOff, errPlatformRepairFailed, errPlatformRoleSquatted} {
		if errors.Is(err, reason) {
			return true
		}
	}
	return false
}
