package config

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// defaultLogRetention is the chart's logs.retention default: 7 days.
const defaultLogRetention = 7 * 24 * time.Hour

// minLogRetention is Loki's floor: its index period is a day.
const minLogRetention = 24 * time.Hour

var logRetentionHours = regexp.MustCompile(`^[0-9]+h$`)

// LogRetention is how long Loki keeps logs (EXC-659): a project's for its
// plan, platform logs and a plan not named for Default.
type LogRetention struct {
	Default time.Duration
	ByPlan  map[domain.TierType]time.Duration
}

// For is how long the logs of a project on tier are kept.
func (r LogRetention) For(tier domain.TierType) time.Duration {
	if period, ok := r.ByPlan[tier]; ok {
		return period
	}
	return r.Default
}

// ParseLogRetention reads LOG_RETENTION ("168h") and LOG_RETENTION_BY_PLAN
// ("free=24h,standard=168h"), as the chart renders them for Loki.
func ParseLogRetention(platform, byPlan string) (LogRetention, error) {
	retention := LogRetention{Default: defaultLogRetention, ByPlan: map[domain.TierType]time.Duration{}}
	if platform != "" {
		period, err := parseLogPeriod(platform)
		if err != nil {
			return LogRetention{}, err
		}
		retention.Default = period
	}
	if byPlan == "" {
		return retention, nil
	}
	for _, pair := range strings.Split(byPlan, ",") {
		plan, raw, ok := strings.Cut(pair, "=")
		tier := domain.TierType(strings.ToUpper(plan))
		if !ok || plan != strings.ToLower(plan) || !domain.IsValidTier(tier) {
			return LogRetention{}, fmt.Errorf("%q is not plan=hours for free, standard or enterprise", pair)
		}
		if _, seen := retention.ByPlan[tier]; seen {
			return LogRetention{}, fmt.Errorf("plan %s named twice", plan)
		}
		period, err := parseLogPeriod(raw)
		if err != nil {
			return LogRetention{}, fmt.Errorf("plan %s: %w", plan, err)
		}
		retention.ByPlan[tier] = period
	}
	return retention, nil
}

func parseLogPeriod(raw string) (time.Duration, error) {
	if !logRetentionHours.MatchString(raw) {
		return 0, fmt.Errorf("%q must be whole hours such as 168h", raw)
	}
	hours, err := strconv.Atoi(strings.TrimSuffix(raw, "h"))
	if err != nil {
		return 0, fmt.Errorf("%q: %w", raw, err)
	}
	period := time.Duration(hours) * time.Hour
	if period < minLogRetention {
		return 0, fmt.Errorf("%q is under 24h, which Loki cannot keep", raw)
	}
	return period, nil
}

func loadLogRetention() LogRetention {
	retention, err := ParseLogRetention(os.Getenv("LOG_RETENTION"), os.Getenv("LOG_RETENTION_BY_PLAN"))
	if err != nil {
		log.Fatalf("LOG_RETENTION / LOG_RETENTION_BY_PLAN: %v", err)
	}
	return retention
}
