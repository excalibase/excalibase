package service

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ErrInvalidMaintenanceWindow refuses a window that is not "<day> HH:MM"
// with a duration from one minute to a day. The window is recorded and shown
// back; nothing schedules maintenance by it yet.
var ErrInvalidMaintenanceWindow = errors.New("invalid maintenance window")

// MaxMaintenanceWindowMinutes is the longest window: one day.
const MaxMaintenanceWindowMinutes = 24 * 60

var maintenanceWindowPattern = regexp.MustCompile(`^([a-z]+) ([0-9]{1,2}):([0-9]{2})$`)

var weekdays = map[string]string{
	"monday": "monday", "tuesday": "tuesday", "wednesday": "wednesday", "thursday": "thursday",
	"friday": "friday", "saturday": "saturday", "sunday": "sunday",
	"mon": "monday", "tue": "tuesday", "wed": "wednesday", "thu": "thursday",
	"fri": "friday", "sat": "saturday", "sun": "sunday",
}

// normalizeMaintenanceWindow checks a window and returns it in its canonical
// spelling, such as "sunday 02:00" (UTC). An empty window with no duration
// clears it.
func normalizeMaintenanceWindow(cfg domain.MaintenanceWindowConfig) (domain.MaintenanceWindowConfig, error) {
	window := strings.ToLower(strings.TrimSpace(cfg.Window))
	if window == "" && cfg.DurationMinutes == 0 {
		cfg.Window = ""
		return cfg, nil
	}
	match := maintenanceWindowPattern.FindStringSubmatch(window)
	if match == nil || weekdays[match[1]] == "" {
		return cfg, fmt.Errorf("%w: window must be a day and a UTC time, such as \"sunday 02:00\"", ErrInvalidMaintenanceWindow)
	}
	hour, _ := strconv.Atoi(match[2])
	minute, _ := strconv.Atoi(match[3])
	if hour > 23 || minute > 59 {
		return cfg, fmt.Errorf("%w: the time must be from 00:00 to 23:59", ErrInvalidMaintenanceWindow)
	}
	if cfg.DurationMinutes < 1 || cfg.DurationMinutes > MaxMaintenanceWindowMinutes {
		return cfg, fmt.Errorf("%w: durationMinutes must be from 1 to %d", ErrInvalidMaintenanceWindow, MaxMaintenanceWindowMinutes)
	}
	cfg.Window = fmt.Sprintf("%s %02d:%02d", weekdays[match[1]], hour, minute)
	return cfg, nil
}
