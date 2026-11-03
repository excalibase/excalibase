package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// defaultHostLogDailyMB is each project's log text a single host stores per day.
const defaultHostLogDailyMB = 64

// maxHostLogDailyMB bounds HOST_LOGS_DAILY_MB at 100 GiB.
const maxHostLogDailyMB = 100 << 10

// HostLogs is a single host's stored log search (EXC-672).
type HostLogs struct {
	// LokiURL is the bundled Loki; empty leaves the search on the engine log.
	LokiURL string
	// DailyBytesPerProject is how much of a project's output is stored per UTC day.
	DailyBytesPerProject int64
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

func loadHostLogs() HostLogs {
	settings, err := ParseHostLogs(os.Getenv("HOST_LOGS_LOKI_URL"), os.Getenv("HOST_LOGS_DAILY_MB"))
	if err != nil {
		log.Fatalf("host logs: %v", err)
	}
	return settings
}
