package apphost

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestEnvSummaries_NamesAndKindsOnlyNoValues(t *testing.T) {
	secret := "sk_live_should_never_appear"
	env := []EnvVar{
		{Name: "PORT", Kind: KindLiteral, Value: strPtr("8080")},
		{Name: "STRIPE_KEY", Kind: KindLiteral, Value: &secret},
		{Name: "DATABASE_URL", Kind: KindReference, Reference: &ReferenceTarget{
			SourceKind: SourceDatabase, SourceName: "db1", Variable: "DATABASE_URL",
		}},
	}

	summaries := EnvSummaries(env)

	want := []EnvSummary{
		{Name: "PORT", Kind: KindLiteral},
		{Name: "STRIPE_KEY", Kind: KindLiteral},
		{Name: "DATABASE_URL", Kind: KindReference},
	}
	if len(summaries) != len(want) {
		t.Fatalf("summaries: got %+v want %+v", summaries, want)
	}
	for i, w := range want {
		if summaries[i] != w {
			t.Errorf("summaries[%d]: got %+v want %+v", i, summaries[i], w)
		}
	}
	blob, err := json.Marshal(summaries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "8080") || strings.Contains(string(blob), secret) {
		t.Fatalf("EnvSummaries must never carry a value, got %s", blob)
	}
}

func TestEnvSummaries_EmptyForNoEnv(t *testing.T) {
	summaries := EnvSummaries(nil)
	if len(summaries) != 0 {
		t.Fatalf("expected no summaries, got %v", summaries)
	}
}

func TestConfigFromApp_CapturesEnvAndFields(t *testing.T) {
	secret := "sk_live_should_be_frozen"
	app := &App{
		ID: "app1", ProjectID: "proj1", Name: "storefront",
		Image: "ghcr.io/acme/storefront:1.4.2",
		Env: []EnvVar{
			{Name: "API_KEY", Kind: KindLiteral, Value: &secret},
			{Name: "DATABASE_URL", Kind: KindReference, Reference: &ReferenceTarget{
				SourceKind: SourceDatabase, SourceName: "db1", Variable: "DATABASE_URL",
			}},
		},
		Port: 8080, HealthCheckPath: "/healthz", Replicas: 2, Tier: domain.Standard,
	}

	config := ConfigFromApp(app)

	if config.Image != app.Image || config.Port != app.Port || config.HealthCheckPath != app.HealthCheckPath ||
		config.Replicas != app.Replicas || config.Tier != app.Tier {
		t.Fatalf("config fields: got %+v", config)
	}
	if len(config.Env) != 2 || config.Env[0].Value == nil || *config.Env[0].Value != secret {
		t.Fatalf("config env: got %+v", config.Env)
	}
	config.Env[0].Name = "MUTATED"
	if app.Env[0].Name == "MUTATED" {
		t.Fatal("ConfigFromApp must copy the env slice, not alias the app's")
	}
}

func TestDeployConfig_ToApp_BuildsRenderableApp(t *testing.T) {
	value := "prod"
	config := DeployConfig{
		Image: "ghcr.io/acme/storefront:1.4.2",
		Env:   []EnvVar{{Name: "MODE", Kind: KindLiteral, Value: &value}},
		Port:  8080, HealthCheckPath: "/healthz", Replicas: 1, Tier: domain.Free,
	}

	app := config.ToApp("app1", "proj1", "storefront")

	if app.ID != "app1" || app.ProjectID != "proj1" || app.Name != "storefront" {
		t.Fatalf("identity: got %+v", app)
	}
	if app.Image != config.Image || app.Port != config.Port || app.Replicas != config.Replicas ||
		app.HealthCheckPath != config.HealthCheckPath || app.Tier != config.Tier {
		t.Fatalf("frozen fields: got %+v", app)
	}
	if len(app.Env) != 1 || app.Env[0].Name != "MODE" {
		t.Fatalf("env: got %+v", app.Env)
	}
	if err := app.Validate(); err != nil {
		t.Fatalf("an app built from a frozen config must validate: %v", err)
	}
}

func strPtr(s string) *string { return &s }
