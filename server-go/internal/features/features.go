// Package features decides whether a feature that ships dark is on (EXC-554).
// Feature code asks Flags with the request's context and never reads the
// environment, so per-org overrides or a flag service can replace Static
// later without touching it.
package features

import (
	"context"
	"net/http"
)

type Feature string

const (
	// MCP is the hosted MCP endpoint for coding tools and its Studio pages (EXC-544).
	MCP Feature = "mcp"
	// Pipeline is CI deploy by image, deploy status polling and auto-deploy (EXC-542/543/545).
	Pipeline Feature = "pipeline"
	// AppStorage is the app-user storage API at /storage/v1/{projectId} and its bucket access rules (EXC-560).
	AppStorage Feature = "appstorage"
	// MySQL is creating MySQL projects on MOCO and their Studio card (ADR 0041, EXC-583).
	MySQL Feature = "mysql"
	// MySQLPITR ships MySQL binlogs between backups, so a point-in-time
	// restore reaches about a minute ago instead of the last backup (EXC-587).
	MySQLPITR Feature = "mysqlpitr"
	// SlowQueries is the slow-queries page and tool, and pg_stat_statements
	// on the clusters it creates (EXC-654).
	SlowQueries Feature = "slowqueries"
	// RequestMetrics is the request charts: graphql and auth scraped into
	// per-project request, error and latency history (EXC-656).
	RequestMetrics Feature = "requestmetrics"
	// Advisors are a project's security and performance checks, in Studio and MCP (EXC-655).
	Advisors Feature = "advisors"
	// MetricsEndpoint is each project's Prometheus metrics endpoint and the
	// metrics keys that read it (EXC-657).
	MetricsEndpoint Feature = "metricsendpoint"
	// LogRetention keeps each project's logs for its plan's period: provisioning
	// labels project namespaces with their plan and clamps log searches (EXC-659).
	LogRetention Feature = "logretention"
	// LogDrains forwards a project's logs to Datadog or an HTTPS endpoint its
	// Admins configure, through the log-forwarder pod (EXC-658).
	LogDrains Feature = "logdrains"
	// AuthOAuth is end-user Google and GitHub sign-in for customers' apps:
	// provider settings, redirect URLs and the new-user switch (EXC-662).
	AuthOAuth Feature = "authoauth"
	// AuthOTP is end-user sign-in by emailed link or six-digit code: the
	// project's switch for it and the sign-in policy it shares with AuthOAuth.
	AuthOTP Feature = "authotp"
	// AuthUser is end-user account self-service: metadata, profile and email
	// change, sign-out scopes, and Studio's per-user details and delete.
	AuthUser Feature = "authuser"
	// AuthGuest is end-user guest accounts (no address, role guest) and the
	// stable token subject: sub becomes the account id.
	AuthGuest Feature = "authguest"
	// DBCopy is copying a database into a new project from now, its latest
	// backup or a time, in Studio and the API (EXC-667).
	DBCopy Feature = "dbcopy"
)

// All is every declared feature; Studio is told the state of each.
func All() []Feature {
	return []Feature{MCP, Pipeline, AppStorage, MySQL, MySQLPITR, SlowQueries, RequestMetrics, Advisors, MetricsEndpoint, LogRetention, LogDrains, AuthOAuth, AuthOTP, AuthUser, AuthGuest, DBCopy}
}

type Flags interface {
	Enabled(ctx context.Context, feature Feature) bool
}

// Static is decided once at startup, the same for every caller.
type Static struct {
	on map[Feature]bool
}

// NewStatic switches on the named features; an undeclared name stays off.
func NewStatic(on ...Feature) Static {
	declared := map[Feature]bool{}
	for _, feature := range All() {
		declared[feature] = true
	}
	enabled := map[Feature]bool{}
	for _, feature := range on {
		if declared[feature] {
			enabled[feature] = true
		}
	}
	return Static{on: enabled}
}

func (s Static) Enabled(_ context.Context, feature Feature) bool {
	return s.on[feature]
}

// Enabled treats missing flags as everything off.
func Enabled(ctx context.Context, flags Flags, feature Feature) bool {
	return flags != nil && flags.Enabled(ctx, feature)
}

// Snapshot is the state of every declared feature, keyed by name.
func Snapshot(ctx context.Context, flags Flags) map[string]bool {
	out := make(map[string]bool, len(All()))
	for _, feature := range All() {
		out[string(feature)] = Enabled(ctx, flags, feature)
	}
	return out
}

// Require answers a route of a feature that is off exactly as an unmounted route.
func Require(flags Flags, feature Feature) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !Enabled(r.Context(), flags, feature) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WhenOn applies middleware only while the feature is on, decided per request.
func WhenOn(flags Flags, feature Feature, middleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return WhenAnyOn(flags, []Feature{feature}, middleware)
}

// WhenAnyOn applies middleware while any of the features is on, decided per request.
func WhenAnyOn(flags Flags, anyOf []Feature, middleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		gated := middleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, feature := range anyOf {
				if Enabled(r.Context(), flags, feature) {
					gated.ServeHTTP(w, r)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
