package sites

import (
	"context"
	"encoding/json"
	"fmt"
)

// routeEntry is a server site's SITE_HOSTS value, read by edge/sites-router hostEntry.
// Sites have no pause of their own yet, so Paused is always false.
type routeEntry struct {
	Script      string `json:"script"`
	Project     string `json:"project"`
	CPUMs       int    `json:"cpuMs"`
	SubRequests int    `json:"subRequests"`
	Paused      bool   `json:"paused"`
}

// RouteEntry is what the router's SITE_HOSTS holds for one of the site's
// hostnames: the script name for a static site, or a JSON entry carrying the
// project and the plan's per-request limits for a server site.
func RouteEntry(site *Site, limits Limits) (string, error) {
	script := ScriptName(site)
	if site.Kind != KindServer {
		return script, nil
	}
	if limits.MaxServerCPUMillis <= 0 || limits.MaxServerSubrequests <= 0 {
		return "", fmt.Errorf("server site %s has no CPU or subrequest limit to route under", site.ID)
	}
	encoded, err := json.Marshal(routeEntry{Script: script, Project: site.ProjectID,
		CPUMs: limits.MaxServerCPUMillis, SubRequests: limits.MaxServerSubrequests})
	if err != nil {
		return "", fmt.Errorf("encode site %s's route: %w", site.ID, err)
	}
	return string(encoded), nil
}

// routeEntryFor reads the plan's limits now, the same source a publish uses.
func (s *Service) routeEntryFor(ctx context.Context, site *Site) (string, error) {
	if site.Kind != KindServer {
		return ScriptName(site), nil
	}
	limits, err := s.limitsFor(ctx, site.ProjectID)
	if err != nil {
		return "", err
	}
	return RouteEntry(site, limits)
}
