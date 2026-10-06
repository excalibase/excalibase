package scheduler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"
)

// ErrInvalidSchedule refuses a cron registry schedule the runner would never
// run as written.
var ErrInvalidSchedule = errors.New("invalid cron schedule")

// cronExpressionParser reads the standard five fields (minute hour
// day-of-month month day-of-week) and the descriptors in cronDescriptors.
// The runner and the deploy-time check share it, so what deploys is what runs.
var cronExpressionParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// cronDescriptors are the shorthands a "cron" schedule may use.
var cronDescriptors = map[string]bool{"@hourly": true, "@daily": true, "@weekly": true, "@monthly": true}

// scheduleSpec is a registry schedule as the bundle writes it.
type scheduleSpec struct {
	Kind       string  `json:"kind"`
	Expression string  `json:"expression"`
	Hours      float64 `json:"hours"`
	Minutes    float64 `json:"minutes"`
	Seconds    float64 `json:"seconds"`
	HourUTC    float64 `json:"hourUTC"`
	MinuteUTC  float64 `json:"minuteUTC"`
}

// ValidateSchedule refuses a schedule whose kind is unknown or whose values
// are out of range, so a bad schedule fails the deploy instead of being saved
// and never firing.
func ValidateSchedule(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("%w: a schedule is required", ErrInvalidSchedule)
	}
	if trimmed[0] != '{' {
		return fmt.Errorf("%w: the schedule must be an object", ErrInvalidSchedule)
	}
	var spec scheduleSpec
	if err := json.Unmarshal(trimmed, &spec); err != nil {
		return fmt.Errorf("%w: the schedule must be an object with numeric fields", ErrInvalidSchedule)
	}
	switch spec.Kind {
	case "cron":
		return validateCronExpression(spec.Expression)
	case "interval":
		return validateInterval(spec)
	case "daily":
		if err := wholeWithin("hourUTC", spec.HourUTC, 0, 23); err != nil {
			return err
		}
		return wholeWithin("minuteUTC", spec.MinuteUTC, 0, 59)
	case "hourly":
		return wholeWithin("minuteUTC", spec.MinuteUTC, 0, 59)
	default:
		return fmt.Errorf("%w: unknown schedule kind %q (want cron, interval, daily or hourly)", ErrInvalidSchedule, spec.Kind)
	}
}

func validateCronExpression(expression string) error {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return fmt.Errorf("%w: a cron schedule needs an expression", ErrInvalidSchedule)
	}
	if strings.HasPrefix(expression, "@") {
		if strings.HasPrefix(expression, "@every") {
			return fmt.Errorf("%w: %q: use an interval schedule for a fixed period", ErrInvalidSchedule, expression)
		}
		if !cronDescriptors[expression] {
			return fmt.Errorf("%w: %q: the shorthands are @hourly, @daily, @weekly and @monthly", ErrInvalidSchedule, expression)
		}
		return nil
	}
	if fields := len(strings.Fields(expression)); fields != 5 {
		return fmt.Errorf("%w: %q must have five fields (minute hour day-of-month month day-of-week), got %d", ErrInvalidSchedule, expression, fields)
	}
	if _, err := cronExpressionParser.Parse(expression); err != nil {
		return fmt.Errorf("%w: cron %q: %v", ErrInvalidSchedule, expression, err)
	}
	return nil
}

func validateInterval(spec scheduleSpec) error {
	for _, part := range []struct {
		name  string
		value float64
	}{{"hours", spec.Hours}, {"minutes", spec.Minutes}, {"seconds", spec.Seconds}} {
		if part.value < 0 {
			return fmt.Errorf("%w: interval %s must not be negative", ErrInvalidSchedule, part.name)
		}
		if part.value != float64(int64(part.value)) {
			return fmt.Errorf("%w: interval %s must be a whole number", ErrInvalidSchedule, part.name)
		}
	}
	if spec.Hours+spec.Minutes+spec.Seconds == 0 {
		return fmt.Errorf("%w: an interval needs a period longer than zero", ErrInvalidSchedule)
	}
	return nil
}

func wholeWithin(name string, value float64, low, high int) error {
	if value != float64(int64(value)) || value < float64(low) || value > float64(high) {
		return fmt.Errorf("%w: %s must be a whole number from %d to %d", ErrInvalidSchedule, name, low, high)
	}
	return nil
}
