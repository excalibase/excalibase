package apphost_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func validApp() *apphost.App {
	value := "production"
	return &apphost.App{
		ID:              "app_01",
		ProjectID:       "proj_abc123",
		Name:            "storefront",
		Image:           "ghcr.io/acme/storefront:1.4.2",
		Env:             []apphost.EnvVar{{Name: "MODE", Kind: apphost.KindLiteral, Value: &value}},
		Port:            8080,
		HealthCheckPath: "/healthz",
		Replicas:        1,
		Tier:            domain.Standard,
		Status:          apphost.StatusCreated,
	}
}

func TestValidateAcceptsAWellFormedApp(t *testing.T) {
	if err := validApp().Validate(); err != nil {
		t.Fatalf("valid app refused: %v", err)
	}
}

// The image reference is stored exactly as given, so what is stored has to be
// a reference a registry can resolve. An implicit tag is a guessed value, not
// a reference, so it is refused rather than defaulted to :latest.
func TestValidateImageReference(t *testing.T) {
	accepted := []string{
		"ghcr.io/acme/storefront:1.4.2",
		"docker.io/library/nginx:1.27-alpine",
		"registry.example.com:5000/team/sub/app:v2",
		"acme/storefront@sha256:" + strings.Repeat("a", 64),
		"localhost:5000/app:dev",
		"nginx:1.27",
	}
	for _, ref := range accepted {
		if err := apphost.ValidateImageReference(ref); err != nil {
			t.Errorf("%q must be accepted: %v", ref, err)
		}
	}

	refused := []string{
		"",
		"nginx",                             // no tag and no digest
		"ghcr.io/acme/storefront:",          // empty tag
		"ghcr.io/acme/storefront@",          // empty digest
		"ghcr.io/acme/storefront@sha256:zz", // not a digest
		"ghcr.io/ACME/storefront:1.0",       // uppercase path component
		" ghcr.io/acme/app:1.0",             // leading space
		"ghcr.io/acme/app:1.0 ",             // trailing space
		"ghcr.io/acme/app:1.0\nrm -rf",      // embedded newline
		"ghcr.io//acme/app:1.0",             // empty path component
		"ghcr.io/acme/app:" + strings.Repeat("t", 129),
		strings.Repeat("a", 600) + ":1.0",
	}
	for _, ref := range refused {
		if err := apphost.ValidateImageReference(ref); err == nil {
			t.Errorf("%q must be refused", ref)
		}
	}
}

func TestValidatePort(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 100000} {
		app := validApp()
		app.Port = port
		if err := app.Validate(); err == nil {
			t.Errorf("port %d must be refused", port)
		}
	}
	for _, port := range []int{1, 8080, 65535} {
		app := validApp()
		app.Port = port
		if err := app.Validate(); err != nil {
			t.Errorf("port %d must be accepted: %v", port, err)
		}
	}
}

// Zero replicas means intentionally stopped, and is legal.
func TestValidateReplicas(t *testing.T) {
	for _, replicas := range []int{0, 1, 2, 3} {
		app := validApp()
		app.Replicas = replicas
		app.Status = apphost.StatusFor(replicas)
		if err := app.Validate(); err != nil {
			t.Errorf("replicas %d must be accepted: %v", replicas, err)
		}
	}
	for _, replicas := range []int{-1, 4, 100} {
		app := validApp()
		app.Replicas = replicas
		if err := app.Validate(); err == nil {
			t.Errorf("replicas %d must be refused", replicas)
		}
	}
}

func TestValidateHealthCheckPath(t *testing.T) {
	app := validApp()
	app.HealthCheckPath = ""
	if err := app.Validate(); err != nil {
		t.Errorf("an absent health check path must be accepted: %v", err)
	}
	for _, path := range []string{"healthz", "healthz/", "/health z", "/health?x=1", "/health#f", "/health\n"} {
		app := validApp()
		app.HealthCheckPath = path
		if err := app.Validate(); err == nil {
			t.Errorf("health check path %q must be refused", path)
		}
	}
}

// An unknown tier is a refusal, never a default: the renderer resolves cpu and
// memory from the catalog and has nothing to fall back on.
func TestValidateTier(t *testing.T) {
	app := validApp()
	app.Tier = domain.TierType("PLATINUM")
	if err := app.Validate(); err == nil {
		t.Error("an unknown tier must be refused")
	}
	app.Tier = ""
	if err := app.Validate(); err == nil {
		t.Error("a missing tier must be refused")
	}
}

func TestValidateName(t *testing.T) {
	for _, name := range []string{"", "A", "UPPER", "with space", "-leading", strings.Repeat("a", 51)} {
		app := validApp()
		app.Name = name
		if err := app.Validate(); err == nil {
			t.Errorf("name %q must be refused", name)
		}
	}
}

func TestValidateIdentifiers(t *testing.T) {
	app := validApp()
	app.ID = "../etc"
	if err := app.Validate(); err == nil {
		t.Error("a traversal app id must be refused")
	}
	app = validApp()
	app.ProjectID = "proj/../other"
	if err := app.Validate(); err == nil {
		t.Error("a traversal project id must be refused")
	}
}

// A variable's kind is declared, never inferred from which payload happens to
// be filled in: the payload must be the one the kind names, and only that one.
func TestValidateVariableKindMatchesItsPayload(t *testing.T) {
	value := "x"
	secret := &apphost.SecretRef{Path: "projects/proj_abc123/apps/app_01/secrets", Key: "token"}
	reference := &apphost.ReferenceTarget{
		SourceKind: apphost.SourceDatabase, SourceName: "storefront_db", Variable: "DATABASE_URL",
	}

	refused := []apphost.EnvVar{
		{Name: "A", Value: &value},                                            // kind omitted
		{Name: "A", Kind: "env", Value: &value},                               // unknown kind
		{Name: "A", Kind: apphost.KindLiteral},                                // no payload
		{Name: "A", Kind: apphost.KindSecret, Value: &value},                  // wrong payload
		{Name: "A", Kind: apphost.KindReference, Secret: secret},              // wrong payload
		{Name: "A", Kind: apphost.KindLiteral, Value: &value, Secret: secret}, // two payloads
		{Name: "A", Kind: apphost.KindSecret, Secret: secret, Reference: reference},
	}
	for _, v := range refused {
		app := validApp()
		app.Env = []apphost.EnvVar{v}
		if err := app.Validate(); err == nil {
			t.Errorf("variable %+v must be refused", v)
		}
	}

	accepted := []apphost.EnvVar{
		{Name: "A", Kind: apphost.KindLiteral, Value: &value},
		{Name: "B", Kind: apphost.KindSecret, Secret: secret},
		{Name: "C", Kind: apphost.KindReference, Reference: reference},
	}
	app := validApp()
	app.Env = accepted
	if err := app.Validate(); err != nil {
		t.Errorf("one variable of each kind must be accepted: %v", err)
	}
}

// The empty string is a legitimate literal value and must not be read as
// absent — an absent value is a variable with no payload, which is refused.
func TestValidateAcceptsEmptyLiteral(t *testing.T) {
	empty := ""
	app := validApp()
	app.Env = []apphost.EnvVar{{Name: "FEATURE_FLAGS", Kind: apphost.KindLiteral, Value: &empty}}
	if err := app.Validate(); err != nil {
		t.Fatalf("an empty literal must be accepted: %v", err)
	}
}

func TestValidateEnvNamesAndDuplicates(t *testing.T) {
	value := "x"
	for _, name := range []string{"", "1LEADING", "has-hyphen", "has space", "lower_ok=", strings.Repeat("A", apphost.MaxEnvNameLength+1)} {
		app := validApp()
		app.Env = []apphost.EnvVar{{Name: name, Kind: apphost.KindLiteral, Value: &value}}
		if err := app.Validate(); err == nil {
			t.Errorf("env name %q must be refused", name)
		}
	}
	app := validApp()
	app.Env = []apphost.EnvVar{
		{Name: "DUP", Kind: apphost.KindLiteral, Value: &value},
		{Name: "DUP", Kind: apphost.KindLiteral, Value: &value},
	}
	if err := app.Validate(); err == nil {
		t.Error("a duplicate env name must be refused")
	}
}

// A reference names its target structurally — source kind, source name and the
// variable on that source — so nothing downstream has to parse a template
// string to find out what was meant.
func TestValidateReferenceTargetShape(t *testing.T) {
	refused := []apphost.ReferenceTarget{
		{SourceKind: "", SourceName: "storefront_db", Variable: "DATABASE_URL"},
		{SourceKind: "cache", SourceName: "storefront_db", Variable: "DATABASE_URL"},
		{SourceKind: apphost.SourceDatabase, SourceName: "", Variable: "DATABASE_URL"},
		{SourceKind: apphost.SourceDatabase, SourceName: "bad name", Variable: "DATABASE_URL"},
		{SourceKind: apphost.SourceDatabase, SourceName: "storefront_db", Variable: ""},
		{SourceKind: apphost.SourceDatabase, SourceName: "storefront_db", Variable: "PGSECRET_SAUCE"},
	}
	for _, target := range refused {
		app := validApp()
		app.Env = []apphost.EnvVar{{Name: "DB", Kind: apphost.KindReference, Reference: &target}}
		if err := app.Validate(); err == nil {
			t.Errorf("reference %+v must be refused", target)
		}
	}

	for _, variable := range apphost.DatabaseSourceVariables() {
		app := validApp()
		app.Env = []apphost.EnvVar{{Name: "DB", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
			SourceKind: apphost.SourceDatabase, SourceName: "storefront_db", Variable: variable,
		}}}
		if err := app.Validate(); err != nil {
			t.Errorf("database variable %q must be referenceable: %v", variable, err)
		}
	}
}

// fixedSources is a SourceLookup over a known set of sources.
type fixedSources map[string]bool

func (f fixedSources) HasSource(projectID string, kind apphost.SourceKind, name string) (bool, error) {
	return f[projectID+"/"+string(kind)+"/"+name], nil
}

// An unresolvable reference is fatal. It is never an empty value and never a
// default: an app that references a database the project does not have is
// refused, and the refusal names the reference.
func TestValidateReferencesRefusesAnUnknownSource(t *testing.T) {
	sources := fixedSources{"proj_abc123/database/storefront_db": true}

	app := validApp()
	app.Env = []apphost.EnvVar{{Name: "DATABASE_URL", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
		SourceKind: apphost.SourceDatabase, SourceName: "storefront_db", Variable: "DATABASE_URL",
	}}}
	if err := apphost.ValidateReferences(app, sources); err != nil {
		t.Fatalf("a reference to an existing source must resolve: %v", err)
	}

	app.Env[0].Reference.SourceName = "nowhere_db"
	err := apphost.ValidateReferences(app, sources)
	if err == nil {
		t.Fatal("a reference to a source the project does not have must be refused")
	}
	if !errors.Is(err, apphost.ErrUnresolvedReference) {
		t.Errorf("refusal must be reported as an unresolved reference, got %v", err)
	}
	for _, want := range []string{"DATABASE_URL", "nowhere_db", "database"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q: %v", want, err)
		}
	}
}

// A project with no database simply has nothing to reference: no variable is
// injected on its behalf, and an app that references nothing is fine.
func TestValidateReferencesLeavesNonReferencesAlone(t *testing.T) {
	value := ""
	app := validApp()
	app.Env = []apphost.EnvVar{
		{Name: "MODE", Kind: apphost.KindLiteral, Value: &value},
		{Name: "TOKEN", Kind: apphost.KindSecret, Secret: &apphost.SecretRef{
			Path: "projects/proj_abc123/apps/app_01/secrets", Key: "token",
		}},
	}
	if err := apphost.ValidateReferences(app, fixedSources{}); err != nil {
		t.Fatalf("an app with no references must not need any source: %v", err)
	}
}

// A reference resolves to the project's internal cluster address. The scope is
// carried explicitly so a later ticket can hand out the public endpoint
// instead without changing what is stored.
func TestResolutionsAreInternalByDefault(t *testing.T) {
	value := "x"
	app := validApp()
	app.Env = []apphost.EnvVar{
		{Name: "MODE", Kind: apphost.KindLiteral, Value: &value},
		{Name: "DATABASE_URL", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
			SourceKind: apphost.SourceDatabase, SourceName: "storefront_db", Variable: "DATABASE_URL",
		}},
	}
	resolutions := app.Resolutions()
	if len(resolutions) != 1 {
		t.Fatalf("only reference variables need resolving, got %d", len(resolutions))
	}
	if resolutions[0].Name != "DATABASE_URL" {
		t.Errorf("resolution must name the variable, got %q", resolutions[0].Name)
	}
	if resolutions[0].Scope != apphost.ScopeInternal {
		t.Errorf("a reference must resolve to the internal address, got %q", resolutions[0].Scope)
	}
	if resolutions[0].Target.SourceName != "storefront_db" {
		t.Errorf("resolution must carry the target, got %+v", resolutions[0].Target)
	}
}

// A secret value never lands in the app row: only the path that names it.
func TestSecretValuesAreNotSerialised(t *testing.T) {
	app := validApp()
	app.Env = []apphost.EnvVar{{Name: "TOKEN", Kind: apphost.KindSecret, Secret: &apphost.SecretRef{
		Path: "projects/proj_abc123/apps/app_01/secrets", Key: "token",
	}}}
	blob, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), `"value"`) {
		t.Errorf("a secret variable must serialise without a value field: %s", blob)
	}
}

// A secret is referenced by its vault path, and the path must sit under the
// project's own vault prefix — one tenant must not name another's secret.
func TestValidateSecretRefStaysInsideTheProject(t *testing.T) {
	refused := []apphost.SecretRef{
		{Path: "projects/proj_other/apps/a/secrets", Key: "token"},
		{Path: "projects/proj_abc123/../proj_other/secrets", Key: "token"},
		{Path: "", Key: "token"},
		{Path: "projects/proj_abc123/apps/app_01/secrets", Key: ""},
		{Path: "projects/proj_abc123/apps/app_01/secrets", Key: "bad key"},
		{Path: "projects/proj_abc123", Key: "token"},
	}
	for _, ref := range refused {
		app := validApp()
		app.Env = []apphost.EnvVar{{Name: "TOKEN", Kind: apphost.KindSecret, Secret: &ref}}
		if err := app.Validate(); err == nil {
			t.Errorf("secret ref %+v must be refused", ref)
		}
	}
}

// Every kind round-trips through JSON, and an empty literal survives as an
// empty literal rather than as an absent one.
func TestVariablesRoundTripThroughJSON(t *testing.T) {
	empty := ""
	app := validApp()
	app.Env = []apphost.EnvVar{
		{Name: "EMPTY", Kind: apphost.KindLiteral, Value: &empty},
		{Name: "TOKEN", Kind: apphost.KindSecret, Secret: &apphost.SecretRef{
			Path: "projects/proj_abc123/apps/app_01/secrets", Key: "token",
		}},
		{Name: "DATABASE_URL", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
			SourceKind: apphost.SourceDatabase, SourceName: "storefront_db", Variable: "DATABASE_URL",
		}},
	}
	blob, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got apphost.App
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Env) != 3 {
		t.Fatalf("env length: got %d want 3", len(got.Env))
	}
	if got.Env[0].Kind != apphost.KindLiteral || got.Env[0].Value == nil || *got.Env[0].Value != "" {
		t.Errorf("the empty literal did not survive: %+v", got.Env[0])
	}
	if got.Env[1].Kind != apphost.KindSecret || got.Env[1].Secret.Key != "token" {
		t.Errorf("the secret did not survive: %+v", got.Env[1])
	}
	if got.Env[2].Kind != apphost.KindReference || got.Env[2].Reference.Variable != "DATABASE_URL" {
		t.Errorf("the reference did not survive: %+v", got.Env[2])
	}
	if err := got.Validate(); err != nil {
		t.Errorf("a round-tripped app must still validate: %v", err)
	}
}

// Every cap is refused at its boundary and accepted just below it.
func TestVariableCaps(t *testing.T) {
	value := "x"
	app := validApp()
	app.Env = nil
	for i := 0; i < apphost.MaxEnvVars; i++ {
		app.Env = append(app.Env, apphost.EnvVar{
			Name: fmt.Sprintf("K%d", i), Kind: apphost.KindLiteral, Value: &value,
		})
	}
	if err := app.Validate(); err != nil {
		t.Fatalf("%d variables must be accepted: %v", apphost.MaxEnvVars, err)
	}
	app.Env = append(app.Env, apphost.EnvVar{Name: "ONE_TOO_MANY", Kind: apphost.KindLiteral, Value: &value})
	if err := app.Validate(); err == nil {
		t.Errorf("more than %d variables must be refused", apphost.MaxEnvVars)
	}

	atCap := strings.Repeat("v", apphost.MaxLiteralValueLength)
	app = validApp()
	app.Env = []apphost.EnvVar{{Name: "BIG", Kind: apphost.KindLiteral, Value: &atCap}}
	if err := app.Validate(); err != nil {
		t.Errorf("a literal at the cap must be accepted: %v", err)
	}
	overCap := atCap + "v"
	app.Env = []apphost.EnvVar{{Name: "BIG", Kind: apphost.KindLiteral, Value: &overCap}}
	if err := app.Validate(); err == nil {
		t.Error("a literal over the cap must be refused")
	}

	name := strings.Repeat("N", apphost.MaxEnvNameLength)
	app = validApp()
	app.Env = []apphost.EnvVar{{Name: name, Kind: apphost.KindLiteral, Value: &value}}
	if err := app.Validate(); err != nil {
		t.Errorf("a name at the cap must be accepted: %v", err)
	}
}

// The variables are also capped as a set: a Kubernetes Secret is capped at
// 1 MiB, and the whole set has to fit inside one with room to spare.
func TestTotalVariableBytesCap(t *testing.T) {
	chunk := strings.Repeat("v", apphost.MaxLiteralValueLength)
	app := validApp()
	app.Env = nil
	for i := 0; i < apphost.MaxTotalEnvBytes/apphost.MaxLiteralValueLength+1; i++ {
		value := chunk
		app.Env = append(app.Env, apphost.EnvVar{
			Name: fmt.Sprintf("K%d", i), Kind: apphost.KindLiteral, Value: &value,
		})
	}
	err := app.Validate()
	if err == nil {
		t.Fatal("a variable set over the total cap must be refused")
	}
	if !strings.Contains(err.Error(), "environment") {
		t.Errorf("the refusal must name what was too large: %v", err)
	}
}

// The status of a freshly created app records what is true: the record exists
// and no workload has been observed. Neither reachable status serves traffic.
func TestStatusForReplicas(t *testing.T) {
	if got := apphost.StatusFor(0); got != apphost.StatusStopped {
		t.Errorf("zero replicas must be stopped, got %q", got)
	}
	for _, replicas := range []int{1, 2, 3} {
		if got := apphost.StatusFor(replicas); got != apphost.StatusCreated {
			t.Errorf("%d replicas must be created, got %q", replicas, got)
		}
	}
	for _, status := range []string{apphost.StatusCreated, apphost.StatusStopped} {
		if !apphost.IsNotServable(status) {
			t.Errorf("%q must refuse serving: no workload has been observed", status)
		}
	}
}

func TestValidateRefusesUnknownStatus(t *testing.T) {
	app := validApp()
	app.Status = "DEPLOYING"
	if err := app.Validate(); err == nil {
		t.Error("a status outside the vocabulary must be refused")
	}
}
