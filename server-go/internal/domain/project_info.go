package domain

// ProjectInfo is a flat combined view of a project + its owning org.
// Returned by GET /api/projects/{projectId}/info so callers (notably
// the auth service at JWT-mint time) can embed display names for support
// dashboards/log search without round-tripping multiple endpoints.
type ProjectInfo struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	OrgID       string `json:"orgId"`
	OrgSlug     string `json:"orgSlug"`
	OrgName     string `json:"orgName"`
	// RealtimeAutoEnable controls whether NoSQL auto-create adds new
	// collection tables to the cdc_watcher publication. v1 defaults to
	// true; future: per-project override stored on instances row.
	RealtimeAutoEnable bool `json:"realtimeAutoEnable"`
}
