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
)

// All is every declared feature; Studio is told the state of each.
func All() []Feature {
	return []Feature{MCP, Pipeline}
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
