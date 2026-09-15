package domain

import "time"

// ProjectActivity is the most recent activity observed for a project. It is
// the signal the idle-pause scheduler reads: a project nobody has touched
// (API, Studio, data-plane policy fetches, function invocations) for N days
// is a candidate for pausing. Kept in its own table so the hot write path
// never contends with database_instances.
type ProjectActivity struct {
	ProjectID      string
	LastSeenAt     time.Time
	LastSeenSource string
	// IdleWarnedAt is when the idle-pause warning was issued for the current
	// idle stretch; nil when no warning is outstanding. Fresh activity clears
	// it. A warning older than the effective last-seen time is stale.
	IdleWarnedAt *time.Time
}

// ActivitySource is the closed vocabulary recorded on
// project_activity.last_seen_source. Coarse on purpose: it answers "who last
// touched this project" for support, not per-endpoint analytics. Values are
// only ever produced by the classifier, never taken from a request, and
// String() re-emits them as literals so nothing request-derived reaches a
// log line or a row.
type ActivitySource string

const (
	ActivitySourceAPI            ActivitySource = "api"             // any other project-scoped platform call (Studio, CLI)
	ActivitySourcePolicyFetch    ActivitySource = "policy_fetch"    // excalibase-graphql refreshing RLS/CLS policies — data-plane traffic
	ActivitySourceInfo           ActivitySource = "info"            // auth service minting an end-user JWT
	ActivitySourceFunctions      ActivitySource = "functions"       // function authoring / deploy / Studio invoke
	ActivitySourceFunctionInvoke ActivitySource = "function_invoke" // public or runtime-to-runtime function invocation
	ActivitySourceSchema         ActivitySource = "schema"
	ActivitySourceMigration      ActivitySource = "migration"
	ActivitySourceBackup         ActivitySource = "backup"
	ActivitySourceRealtime       ActivitySource = "realtime"
	ActivitySourceStorage        ActivitySource = "storage"
)

// String returns the canonical literal for a known source. Anything outside
// the vocabulary collapses to "api", so a caller can never smuggle an
// arbitrary string through a typed conversion.
func (s ActivitySource) String() string {
	switch s {
	case ActivitySourcePolicyFetch:
		return "policy_fetch"
	case ActivitySourceInfo:
		return "info"
	case ActivitySourceFunctions:
		return "functions"
	case ActivitySourceFunctionInvoke:
		return "function_invoke"
	case ActivitySourceSchema:
		return "schema"
	case ActivitySourceMigration:
		return "migration"
	case ActivitySourceBackup:
		return "backup"
	case ActivitySourceRealtime:
		return "realtime"
	case ActivitySourceStorage:
		return "storage"
	default:
		return "api"
	}
}
