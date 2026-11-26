package routepolicy

import (
	"strings"
	"testing"
)

// EXC-899: whoever owns an org's apps decides what they may run (the trust of
// Org.OwnerID). Every route that creates, changes or starts an app has to say
// whether it resolves that trust before it acts, so a new app route cannot be
// added without someone choosing. P4 makes "resolves" true in the handlers;
// until then this table is the checklist the route test enforces.
const (
	trustResolves = "resolves"
	trustExempt   = "exempt"
)

type trustClass struct {
	class  string
	reason string
}

var appRouteTrust = map[string]trustClass{
	"POST /api/projects/{projectId}/apps/":                                    {trustResolves, "creates an app"},
	"PATCH /api/projects/{projectId}/apps/{appId}/":                           {trustResolves, "changes what the next deploy runs"},
	"DELETE /api/projects/{projectId}/apps/{appId}/":                          {trustExempt, "removes a workload"},
	"POST /api/projects/{projectId}/apps/{appId}/pause":                       {trustExempt, "scales to zero"},
	"POST /api/projects/{projectId}/apps/{appId}/resume":                      {trustResolves, "starts the workload; never refused for trust, only rendered with the owner's class"},
	"POST /api/projects/{projectId}/apps/{appId}/disk":                        {trustExempt, "plan-capped storage, no network effect"},
	"POST /api/projects/{projectId}/apps/{appId}/deploy":                      {trustResolves, "renders the workload and its ingress"},
	"POST /api/projects/{projectId}/apps/{appId}/deploys/{deployId}/redeploy": {trustResolves, "renders the workload and its ingress"},
	"PUT /api/projects/{projectId}/apps/{appId}/secrets/{name}":               {trustExempt, "stores a secret value only"},
	"PUT /api/projects/{projectId}/app-network/":                              {trustExempt, "private app-to-app traffic, not the public edge"},
	"POST /api/projects/{projectId}/app-templates/{templateId}/deploy":        {trustResolves, "creates apps and their deploys"},
	"PUT /api/projects/{projectId}/registry-credentials/{registry}":           {trustExempt, "stores a credential"},
	"DELETE /api/projects/{projectId}/registry-credentials/{registry}":        {trustExempt, "removes a credential"},
	"POST /api/projects/{projectId}/apps/{appId}/builds":                      {trustResolves, "builds an image that is then rolled out as a deploy"},
	"PUT /api/projects/{projectId}/apps/{appId}/git":                          {trustExempt, "stores the app's git source only"},
	"POST /api/projects/{projectId}/apps/{appId}/git/sign-in":                 {trustExempt, "starts a git provider sign-in"},
	"DELETE /api/projects/{projectId}/apps/{appId}/git":                       {trustExempt, "removes the git source"},
	"POST /api/projects/{projectId}/apps/{appId}/domains/":                    {trustResolves, "adds a host to the app's ingress"},
	"POST /api/projects/{projectId}/apps/{appId}/domains/{domainId}/verify":   {trustResolves, "routes the host: writes the ingress"},
	"DELETE /api/projects/{projectId}/apps/{appId}/domains/{domainId}":        {trustResolves, "rewrites the ingress without the host"},
}

func appWriteRoutes() map[string]bool {
	routes := map[string]bool{}
	for _, row := range Table() {
		if !strings.Contains(row.Pattern, "/apps/") && !strings.Contains(row.Pattern, "/app-templates/") &&
			!strings.Contains(row.Pattern, "/app-network/") && !strings.Contains(row.Pattern, "/registry-credentials/") {
			continue
		}
		for _, method := range row.Methods {
			if method != "GET" {
				routes[method+" "+row.Pattern] = true
			}
		}
	}
	return routes
}

func TestEveryAppWriteRouteIsClassifiedForTrust(t *testing.T) {
	routes := appWriteRoutes()
	if len(routes) == 0 {
		t.Fatal("found no app write routes; the filter is wrong")
	}
	for route := range routes {
		class, ok := appRouteTrust[route]
		if !ok {
			t.Errorf("%s is an app write route with no trust class; add it to appRouteTrust as resolves or exempt", route)
			continue
		}
		if class.reason == "" || (class.class != trustResolves && class.class != trustExempt) {
			t.Errorf("%s: class %q needs resolves or exempt and a reason", route, class.class)
		}
	}
	for route := range appRouteTrust {
		if !routes[route] {
			t.Errorf("appRouteTrust names %s, which is no longer a route", route)
		}
	}
}
