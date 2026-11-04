package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// defaultHostLogDailyMB is each project's log text a single host stores per day.
const defaultHostLogDailyMB = 64

// maxHostLogDailyMB bounds HOST_LOGS_DAILY_MB at 100 GiB.
const maxHostLogDailyMB = 100 << 10

// defaultHostLogMinFreePercent pauses shipping below a tenth of the disk free (EXC-678).
const defaultHostLogMinFreePercent = 10

// defaultHostLogDiskPath is provisioning's own volume, on the filesystem that
// holds the engine's named volumes, Loki's included.
const defaultHostLogDiskPath = "/var/lib/excalibase"

// HostLogs is a single host's stored log search (EXC-672).
type HostLogs struct {
	// LokiURL is the bundled Loki; empty leaves the search on the engine log.
	LokiURL string
	// DailyBytesPerProject is how much of a project's output is stored per UTC
	// day, unless DailyBytesByPlan names its plan (EXC-678).
	DailyBytesPerProject int64
	DailyBytesByPlan     map[domain.TierType]int64
	// MinFreePercent pauses all shipping while less of DiskPath's filesystem is free.
	MinFreePercent int
	DiskPath       string
}

// ParseHostLogs reads HOST_LOGS_LOKI_URL and HOST_LOGS_DAILY_MB.
func ParseHostLogs(lokiURL, dailyMB string) (HostLogs, error) {
	settings := HostLogs{DailyBytesPerProject: defaultHostLogDailyMB << 20}
	if raw := strings.TrimSpace(lokiURL); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return HostLogs{}, fmt.Errorf("HOST_LOGS_LOKI_URL %q is not an http(s) address such as http://loki:3100", raw)
		}
		settings.LokiURL = strings.TrimRight(raw, "/")
	}
	if dailyMB == "" {
		return settings, nil
	}
	mb, err := strconv.Atoi(dailyMB)
	if err != nil || mb < 1 || mb > maxHostLogDailyMB {
		return HostLogs{}, fmt.Errorf("HOST_LOGS_DAILY_MB %q must be whole MiB from 1 to %d", dailyMB, maxHostLogDailyMB)
	}
	settings.DailyBytesPerProject = int64(mb) << 20
	return settings, nil
}

// ParseHostLogBudgets reads HOST_LOGS_DAILY_MB_BY_PLAN ("free=64,standard=256").
func ParseHostLogBudgets(byPlan string) (map[domain.TierType]int64, error) {
	budgets := map[domain.TierType]int64{}
	if byPlan == "" {
		return budgets, nil
	}
	for _, pair := range strings.Split(byPlan, ",") {
		plan, raw, ok := strings.Cut(pair, "=")
		tier := domain.TierType(strings.ToUpper(plan))
		if !ok || plan != strings.ToLower(plan) || !domain.IsValidTier(tier) {
			return nil, fmt.Errorf("%q is not plan=MiB for free, standard or enterprise", pair)
		}
		if _, seen := budgets[tier]; seen {
			return nil, fmt.Errorf("plan %s named twice", plan)
		}
		mb, err := strconv.Atoi(raw)
		if err != nil || mb < 1 || mb > maxHostLogDailyMB {
			return nil, fmt.Errorf("plan %s: %q must be whole MiB from 1 to %d", plan, raw, maxHostLogDailyMB)
		}
		budgets[tier] = int64(mb) << 20
	}
	return budgets, nil
}

// ParseHostLogMinFreePercent reads HOST_LOGS_MIN_FREE_PERCENT: 1 to 50, 10 when unset.
func ParseHostLogMinFreePercent(raw string) (int, error) {
	if raw == "" {
		return defaultHostLogMinFreePercent, nil
	}
	percent, err := strconv.Atoi(raw)
	if err != nil || percent < 1 || percent > 50 {
		return 0, fmt.Errorf("HOST_LOGS_MIN_FREE_PERCENT %q must be a whole percent from 1 to 50", raw)
	}
	return percent, nil
}

func loadHostLogs() HostLogs {
	settings, err := ParseHostLogs(os.Getenv("HOST_LOGS_LOKI_URL"), os.Getenv("HOST_LOGS_DAILY_MB"))
	if err != nil {
		log.Fatalf("host logs: %v", err)
	}
	if settings.DailyBytesByPlan, err = ParseHostLogBudgets(os.Getenv("HOST_LOGS_DAILY_MB_BY_PLAN")); err != nil {
		log.Fatalf("host logs: HOST_LOGS_DAILY_MB_BY_PLAN: %v", err)
	}
	if settings.MinFreePercent, err = ParseHostLogMinFreePercent(os.Getenv("HOST_LOGS_MIN_FREE_PERCENT")); err != nil {
		log.Fatalf("host logs: %v", err)
	}
	settings.DiskPath = strings.TrimSpace(os.Getenv("HOST_LOGS_DISK_PATH"))
	if settings.DiskPath == "" {
		settings.DiskPath = defaultHostLogDiskPath
	}
	return settings
}
