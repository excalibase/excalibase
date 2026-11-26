package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// errPlatformSchemaLeft is a reconcile that kept every revoke it could and left
// customer-owned objects in a platform schema for an operator (EXC-821).
var errPlatformSchemaLeft = errors.New("a platform schema holds customer objects left for an operator")

// platformSchemaMetric keys the project alert while a schema is left.
const platformSchemaMetric = "project.platform-schema"

// SetSchemaAlerts raises a project alert, in the alerts table the pod-pressure
// and WAL archive alerts use, when the reconcile leaves a platform schema for
// an operator, and resolves it once the schema is clean.
func (s *ProvisioningService) SetSchemaAlerts(alerts *AlertingService) { s.schemaAlerts = alerts }

// schemaLeftError marks a failed reconcile as a schema left for an operator
// when its text carries the refusal's marker.
func schemaLeftError(err error) error {
	if err != nil && !errors.Is(err, errPlatformSchemaLeft) && strings.Contains(err.Error(), platformSchemaLeftMarker) {
		return fmt.Errorf("%w: %v", errPlatformSchemaLeft, err)
	}
	return err
}

// trackSchemaAlert opens the alert while left is true and resolves it when not.
func (s *ProvisioningService) trackSchemaAlert(projectID string, left bool) {
	if s.schemaAlerts == nil {
		return
	}
	s.schemaAlerts.Track(projectID, platformSchemaMetric, left, func() domain.Alert {
		now := time.Now()
		return domain.Alert{
			ID: fmt.Sprintf("%s-%s-%d", platformSchemaMetric, projectID, now.UnixNano()), Severity: "CRITICAL",
			Message: "A customer role owns objects in the project's auth or excalibase_cdc schema that the platform cannot move or adopt. " +
				"Every grant was revoked; an operator must review them (OPERATOR.md 6.9).",
			Timestamp: &domain.FlexTime{Time: now},
		}
	})
}
