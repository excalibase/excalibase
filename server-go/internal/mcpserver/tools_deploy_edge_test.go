package mcpserver

import (
	"net/http"
	"strings"
	"testing"
)

func TestDeployWithAnImageForAMissingAppKeepsTheNotFound(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 404, `{"error":"not found"}`)
	failure, _ := deployWith(t, routes, map[string]any{"image": "ghcr.io/a/web:2"})
	if !strings.Contains(failure, "not found") {
		t.Fatalf("error = %s", failure)
	}
}

func TestDeployRefusalWithAnUnreadableRegistryListKeepsTheRoutesWords(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 422, `{"error":"the registry refused access to the image; save a credential for its registry"}`)
	failure, _ := deployWith(t, routes, map[string]any{"image": "registry.acme.test/team/web:3"})
	if !strings.Contains(failure, "save a credential") {
		t.Fatalf("error = %s", failure)
	}
}
