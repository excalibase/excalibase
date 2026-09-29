package apptemplate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// sequence hands out predictable ids and secrets so a test can find them.
type sequence struct{ ids, secrets int }

func (s *sequence) id() string { s.ids++; return fmt.Sprintf("app-%d", s.ids) }

func (s *sequence) secret(n int) (string, error) {
	s.secrets++
	value := fmt.Sprintf("GENERATED%d", s.secrets)
	return value + strings.Repeat("x", n-len(value)), nil
}

func buildInput(seq *sequence) BuildInput {
	return BuildInput{ProjectID: "proj-1", Tier: domain.Free, DatabaseName: "proj_1_db", NewID: seq.id, Secret: seq.secret}
}

func envOf(t *testing.T, app *apphost.App, name string) apphost.EnvVar {
	t.Helper()
	for _, v := range app.Env {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("%s has no variable %s", app.Name, name)
	return apphost.EnvVar{}
}

func TestBuildWebAndRedis(t *testing.T) {
	seq := &sequence{}
	built, err := mustParse(t, webRedisSource).Build(buildInput(seq))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(built.Apps) != 2 {
		t.Fatalf("%d apps", len(built.Apps))
	}
	redis, web := built.Apps[0], built.Apps[1]
	if redis.ID != "app-1" || redis.ProjectID != "proj-1" || !redis.Internal || redis.Tier != domain.Free ||
		redis.Status != apphost.StatusCreated || redis.Disk == nil || redis.InternalPorts[0] != (apphost.InternalPort{Port: 6379, Protocol: apphost.ProtocolTCP}) {
		t.Fatalf("redis %+v", redis)
	}
	for _, app := range built.Apps {
		if err := app.Validate(); err != nil {
			t.Fatalf("%s is not a valid app: %v", app.Name, err)
		}
	}

	password := envOf(t, redis, "REDIS_PASSWORD")
	if password.Kind != apphost.KindSecret || *password.Secret != apphost.AppSecretRef("proj-1", "app-1", "REDIS_PASSWORD") {
		t.Fatalf("redis password %+v", password)
	}
	generated := built.Secrets[password.Secret.Path]
	if len(generated) != 32 || !strings.HasPrefix(generated, "GENERATED1") {
		t.Fatalf("stored secret %q", generated)
	}
	shared := envOf(t, web, "REDIS_PASSWORD")
	if shared.Kind != apphost.KindSecret || built.Secrets[shared.Secret.Path] != generated {
		t.Fatalf("web must hold redis's password in its own secret entry: %+v", shared)
	}
	if !strings.HasPrefix(shared.Secret.Path, apphost.AppSecretPrefix("proj-1", "app-2")) {
		t.Fatalf("web's copy is under %q", shared.Secret.Path)
	}
	url := envOf(t, web, "REDIS_URL")
	if url.Kind != apphost.KindSecret || built.Secrets[url.Secret.Path] != "redis://:"+generated+"@redis:6379/0" {
		t.Fatalf("redis url %+v = %q", url, built.Secrets[url.Secret.Path])
	}
	for name, want := range map[string]string{"REDIS_HOST": "redis", "REDIS_PORT": "6379", "GREETING": "hello"} {
		if v := envOf(t, web, name); v.Kind != apphost.KindLiteral || *v.Value != want {
			t.Errorf("%s = %+v, want literal %q", name, v, want)
		}
	}
	if seq.secrets != 1 {
		t.Fatalf("generated %d secrets, want exactly 1", seq.secrets)
	}

	// The records the store keeps never hold a generated value.
	for _, app := range built.Apps {
		blob, _ := json.Marshal(app)
		if strings.Contains(string(blob), "GENERATED") {
			t.Fatalf("%s carries a generated value: %s", app.Name, blob)
		}
	}
}

func TestBuildDatabaseReference(t *testing.T) {
	seq := &sequence{}
	built, err := mustParse(t, `
format: excalibase.template/v1
id: web-db
name: Web + DB
summary: s
apps:
  - name: web
    image: nginx:1.27
    port: 8080
    replicas: 1
    env:
      - {name: DATABASE_URL, value: "${{ db.DATABASE_URL }}"}
      - {name: A, value: "${{ secret(16) }}"}
      - {name: B, value: "${{ secret(16) }}"}
      - {name: WEB_PORT, value: "${{ apps.web.port }}"}
`).Build(buildInput(seq))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	web := built.Apps[0]
	ref := envOf(t, web, "DATABASE_URL")
	want := apphost.ReferenceTarget{SourceKind: apphost.SourceDatabase, SourceName: "proj_1_db", Variable: "DATABASE_URL"}
	if ref.Kind != apphost.KindReference || *ref.Reference != want {
		t.Fatalf("reference %+v", ref)
	}
	a, b := envOf(t, web, "A"), envOf(t, web, "B")
	if built.Secrets[a.Secret.Path] == built.Secrets[b.Secret.Path] {
		t.Fatal("every secret() is a value of its own")
	}
	if port := envOf(t, web, "WEB_PORT"); *port.Value != "80" {
		t.Fatalf("a public app is reached on its Service's HTTP port, got %q", *port.Value)
	}
}

func TestBuildRefusesADatabaseReferenceWithNoDatabase(t *testing.T) {
	seq := &sequence{}
	input := buildInput(seq)
	input.DatabaseName = ""
	_, err := mustParse(t, `
format: excalibase.template/v1
id: web-db
name: Web + DB
summary: s
apps:
  - name: web
    image: nginx:1.27
    port: 8080
    replicas: 1
    env:
      - {name: DATABASE_URL, value: "${{ db.DATABASE_URL }}"}
`).Build(input)
	if err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("want a refusal naming the database, got %v", err)
	}
}

func TestBuildNeverNamesAGeneratedValueInAnError(t *testing.T) {
	seq := &sequence{}
	input := buildInput(seq)
	input.NewID = func() string { return "bad id with spaces" }
	_, err := mustParse(t, webRedisSource).Build(input)
	if err == nil {
		t.Fatal("an invalid id must be refused")
	}
	if strings.Contains(err.Error(), "GENERATED") {
		t.Fatalf("the error carries a generated value: %v", err)
	}
}

func TestSecretAlphabetIsLettersAndDigits(t *testing.T) {
	if len(secretAlphabet) != 62 || !strings.HasPrefix(secretAlphabet, "ABC") || !strings.HasSuffix(secretAlphabet, "789") {
		t.Fatalf("alphabet %q", secretAlphabet)
	}
}

func TestGenerateSecret(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		value, err := GenerateSecret(32)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(value) != 32 || strings.Trim(value, secretAlphabet) != "" {
			t.Fatalf("value %q is not 32 characters of the alphabet", value)
		}
		if seen[value] {
			t.Fatalf("repeated value %q", value)
		}
		seen[value] = true
	}
	if _, err := GenerateSecret(MinSecretLength - 1); err == nil {
		t.Fatal("a length below the minimum must be refused")
	}
}
