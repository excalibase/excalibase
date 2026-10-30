// Package logdelete removes a deleted project's stored log lines from Loki
// (EXC-678). The teardown queues one row per project; a worker sends Loki's
// delete request and keeps checking until Loki says it processed it.
package logdelete

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ErrUnsafeSelector is a selector that is not exactly one project's label.
var ErrUnsafeSelector = errors.New("log deletion: not one project's own selector")

var (
	// labelValuePattern is a DNS label: a namespace, or a project id inside one.
	labelValuePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// selectorPattern is the only selector that reaches Loki's delete API.
	selectorPattern = regexp.MustCompile(`^\{(namespace|project_id)="[a-z0-9]([-a-z0-9]*[a-z0-9])?"\}$`)
)

// maxLabelValue is Kubernetes' namespace length limit.
const maxLabelValue = 63

// schemaStart is the first day of both installs' Loki schema.
var schemaStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// ProjectSelector is the project's lines and nothing else, built from its
// row: on Kubernetes its own namespace, which the collector labels every pod
// of; on a single host the project_id label the shipper sets.
func ProjectSelector(inst *domain.DatabaseInstance) (string, error) {
	if inst == nil || !validLabelValue(inst.ProjectID) {
		return "", ErrUnsafeSelector
	}
	switch inst.DeploymentMode {
	case domain.ModeK8s, "":
		ns := inst.Namespace
		if !validLabelValue(ns) || !strings.HasSuffix(ns, "-"+inst.ProjectID) || len(ns) <= len(inst.ProjectID)+1 {
			return "", fmt.Errorf("%w: namespace %q is not project %s's", ErrUnsafeSelector, ns, inst.ProjectID)
		}
		return "{namespace=" + strconv.Quote(ns) + "}", nil
	case domain.ModeDocker:
		return "{project_id=" + strconv.Quote(inst.ProjectID) + "}", nil
	default:
		return "", fmt.Errorf("%w: deployment mode %q", ErrUnsafeSelector, inst.DeploymentMode)
	}
}

func validLabelValue(value string) bool {
	return len(value) <= maxLabelValue && labelValuePattern.MatchString(value)
}

// CheckSelector admits exactly one exact-match project label: never empty, a
// regex, a second label or a line filter.
func CheckSelector(selector string) error {
	if !selectorPattern.MatchString(selector) {
		return ErrUnsafeSelector
	}
	return nil
}

// StartFor is where a deletion's window starts, in whole seconds: a day
// before the project was created, but no further back than the longest
// retention and two days, past which Loki has nothing left.
func StartFor(created, now time.Time, maxAge time.Duration) time.Time {
	start := now.Add(-maxAge - 48*time.Hour)
	if !created.IsZero() && created.Add(-24*time.Hour).After(start) {
		start = created.Add(-24 * time.Hour)
	}
	if start.Before(schemaStart) {
		start = schemaStart
	}
	return start.Truncate(time.Second)
}

// EndFor is where a deletion's window ends: shortly after the teardown, for
// the collector's last lines, and never later.
func EndFor(teardown time.Time) time.Time {
	return teardown.Add(endSlack).Truncate(time.Second)
}
