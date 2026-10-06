package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// Studio's connect snippets name the public API address the SDK, REST and
// GraphQL calls go to: the same base the MCP snippet and function URLs use.
func TestConfigReportsThePublicAPIURL(t *testing.T) {
	handler := serveConfig(config.AppConfig{DeploymentMode: "cloud", PublicBaseURL: "https://api.example.test/"}, features.NewStatic())
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var body struct {
		APIURL string `json:"apiUrl"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.APIURL != "https://api.example.test" {
		t.Fatalf("apiUrl = %q, want the public base without a trailing slash", body.APIURL)
	}
}
