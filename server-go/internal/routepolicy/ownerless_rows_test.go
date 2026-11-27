package routepolicy

import "testing"

// ownerlessSessionRoutes are the only routes any signed-in caller may reach
// with no tenant resource to bind to and no platform permission to hold. Each
// one answers only about the caller themself or about public catalogue data.
//
// Adding a route here is a decision: /api/capacity once sat in this shape
// (Owner: OwnerNone, nothing else) and handed every customer the cluster's CPU,
// memory and per-tier project counts (EXC-761). A route that discloses
// platform data takes a Permission instead.
var ownerlessSessionRoutes = map[string]string{
	"POST /api/email/verify/send":   "mails the caller's own verification flow",
	"POST /api/credits/terms":       "the caller accepts the credit terms for themself",
	"GET /api/tiers/":               "tier specs for the provision page",
	"GET /api/postgres/catalog":     "supported PostgreSQL majors",
	"GET /api/mysql/catalog":        "supported MySQL versions",
	"GET /api/alerts/":              "the handler scopes the list to the caller's projects",
	"GET /api/alerts/history":       "the handler scopes the list to the caller's projects",
	"GET /api/auth/me":              "the caller's own identity",
	"GET /api/orgs/":                "the caller's own orgs",
	"POST /api/orgs/":               "the creator becomes the owner",
	"POST /api/orgs/invites/accept": "the invite token is the credential",
	"GET /api/provision/":           "the handler scopes the list to the caller's orgs",
	"POST /api/provision/":          "the handler checks the caller's org",
	"POST /api/provision/estimate":  "a price calculation over the request's own numbers",
	"POST /internal/email/send":     "refused to anyone but the auth service's token in the handler",
}

func TestOwnerlessSessionRoutesAreAnExplicitList(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range Table() {
		if row.Auth != AuthSession || row.Owner != OwnerNone || row.Permission != "" {
			continue
		}
		for _, method := range row.expandMethods() {
			key := method + " " + row.Pattern
			seen[key] = true
			if _, ok := ownerlessSessionRoutes[key]; !ok {
				t.Errorf("%s names no owner and needs no permission, so any signed-in user may call it: "+
					"give it a Permission or an Owner, or add it to ownerlessSessionRoutes with the reason", key)
			}
		}
	}
	for key := range ownerlessSessionRoutes {
		if !seen[key] {
			t.Errorf("ownerlessSessionRoutes lists %s, which is not an ownerless session route any more", key)
		}
	}
}
