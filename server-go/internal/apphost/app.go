// Package apphost is the customer-application resource: a container image the
// platform runs beside a project's database (EXC-377, EXC-378).
//
// A project holds two independent services. Databases and Apps sit side by
// side the way Compute Engine and Cloud SQL sit under one GCP project — a
// project may hold an app and no database, so nothing here reads, requires or
// implies a provisioned database.
//
// v1 is bring-your-own-image: the customer hands over a registry reference and
// the platform runs it. There is no build pipeline, and this package resolves
// nothing — the reference is stored exactly as given, after being checked to
// be a reference a registry could resolve. ResolvedDigest is the slot the
// deploy (EXC-386) fills once it has actually resolved one; it stays empty
// here.
//
// This package is the resource model and its persistence contract only. It
// renders no Kubernetes object (EXC-379), decides no isolation (EXC-380/381),
// routes no traffic (EXC-383/384) and runs no deploy (EXC-386).
package apphost

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Limits on the resource model. Replicas are capped platform-wide as well as
// per tier (config.AppTierConfig.MaxReplicas); the tighter of the two wins.
const (
	// MaxAppsPerProject is one for now. It is a rule, not a schema
	// constraint — apps are a collection keyed by app id so raising it is a
	// change to this constant, not a migration.
	MaxAppsPerProject = 1
	// MaxReplicas is 3 in v1. Zero replicas is legal and means the app is
	// intentionally stopped.
	MaxReplicas = 3
	// MaxEnvVars caps how many variables one app declares. The number is
	// unbounded from the customer's side, so the cap is explicit and refused
	// at the boundary rather than discovered when a deploy fails.
	MaxEnvVars = 100
	// MaxEnvNameLength caps one variable's name.
	MaxEnvNameLength = 128
	// MaxLiteralValueLength caps one literal value at 8 KiB. The empty
	// string is a legitimate value and is never read as absent.
	MaxLiteralValueLength = 8 * 1024
	// MaxTotalEnvBytes caps the variables together at 64 KiB. A Kubernetes
	// Secret is capped at 1 MiB, and the whole set has to fit inside one
	// with room to spare.
	MaxTotalEnvBytes = 64 * 1024
	// MaxImageRefLength caps the stored reference.
	MaxImageRefLength = 512
	// MaxHealthCheckPathLength caps the probe path.
	MaxHealthCheckPathLength = 256
	// MaxNameLength / minNameLength bound the per-project app name.
	MaxNameLength = 50
	minNameLength = 2
	// MaxIDLength bounds the generated app id.
	MaxIDLength       = 64
	maxImageTagLength = 128
)

// Status vocabulary. It is the project vocabulary, reused verbatim rather than
// re-spelled, and it obeys the same lifecycle-honesty rule: a status is
// written only after the result it names has been observed.
//
// Only two statuses are reachable from this ticket. An app whose record exists
// but whose workload nothing has deployed holds StatusCreated; an app the
// tenant asked for zero replicas of holds StatusStopped. Neither means a
// container is running, so neither serves traffic. The deploy lifecycle that
// reaches StatusRunning is EXC-386's, and nothing here writes it.
const (
	// StatusCreated — the record exists and no workload has been observed.
	StatusCreated = domain.StatusProvisioning
	// StatusStopped — zero replicas were asked for, on purpose.
	StatusStopped = string(domain.StatusPaused)
	// StatusRunning — a deploy observed the workload answering. Unreachable
	// until EXC-386; named here so IsNotServable has something to compare to
	// instead of guessing.
	StatusRunning = "ACTIVE"
)

// validStatuses bounds what may be written to the status column, so a typo
// cannot invent a state the rest of the platform does not understand.
var validStatuses = map[string]bool{
	StatusCreated: true,
	StatusStopped: true,
	StatusRunning: true,
}

// StatusFor is the status an app holds given the replica count it was last
// asked for. It never claims a workload is running: only a deploy that
// observed one may write StatusRunning.
func StatusFor(replicas int) string {
	if replicas == 0 {
		return StatusStopped
	}
	return StatusCreated
}

// IsNotServable reports whether an app must not receive traffic. Only an app a
// deploy has observed running is servable; everything else — including both
// statuses this ticket can reach — refuses. Mirrors domain.IsNotServable for
// projects: the row existing is exactly why every read has to ask.
func IsNotServable(status string) bool { return status != StatusRunning }

// SecretRef names a secret by its vault path instead of carrying its value.
// Secret values are never stored in the app row: they live in the project's
// vault under the same per-project prefix the database credentials use, and
// the deploy reads them at render time.
type SecretRef struct {
	// Path is the vault path, which must sit under the project's own
	// "projects/{projectID}/" prefix.
	Path string `json:"path"`
	// Key is the field within the secret at Path.
	Key string `json:"key"`
}

// VarKind discriminates what a variable carries. It is declared by the
// caller, never inferred from which payload happens to be populated: a
// variable whose kind and payload disagree is refused rather than guessed.
type VarKind string

const (
	// KindLiteral — a plain value stored in the app row. The empty string is
	// a legal value.
	KindLiteral VarKind = "literal"
	// KindReference — a declared pointer to another source in the same
	// project, such as its database.
	KindReference VarKind = "reference"
	// KindSecret — a pointer to a vault path. The value never enters the row.
	KindSecret VarKind = "secret"
)

// SourceKind names a kind of thing a variable may reference. Only the
// project's database is referenceable in v1.
type SourceKind string

// SourceDatabase is the project's provisioned database.
const SourceDatabase SourceKind = "database"

// ReferenceScope selects which address a reference resolves to. A database
// reference resolves to the project's internal cluster address: the public
// per-tenant port is a different address for the same database and an app
// running beside it has no reason to leave the cluster. The scope is carried
// explicitly so EXC-383/384 can offer the public endpoint without changing
// what is stored.
type ReferenceScope string

const (
	ScopeInternal ReferenceScope = "internal"
	ScopePublic   ReferenceScope = "public"
)

// databaseSourceVariables is the fixed set of variables a database source
// exposes. A reference to anything outside it names nothing, so it is
// refused here rather than resolving to an empty string at deploy time.
var databaseSourceVariables = []string{
	"DATABASE_URL", "PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD",
}

// DatabaseSourceVariables returns the variables a database source exposes.
func DatabaseSourceVariables() []string {
	out := make([]string, len(databaseSourceVariables))
	copy(out, databaseSourceVariables)
	return out
}

// ReferenceTarget names what a reference points at, structurally: the kind of
// source, the source's name within the project, and the variable on it. It is
// deliberately not a template string — storing "${{ db.DATABASE_URL }}" would
// leave every reader to parse it, and a parse that goes wrong at deploy time
// is exactly the silent empty value this model exists to prevent.
type ReferenceTarget struct {
	SourceKind SourceKind `json:"sourceKind"`
	SourceName string     `json:"sourceName"`
	Variable   string     `json:"variable"`
}

// String renders the target the way a refusal names it.
func (t ReferenceTarget) String() string {
	return string(t.SourceKind) + " " + t.SourceName + "." + t.Variable
}

// SecretRef and ReferenceTarget are pointers on EnvVar so the payload a kind
// does not name stays absent rather than zero-valued.

// ResolvedReference is what a renderer must produce for one reference
// variable: the variable to set, what it points at, and which address it
// resolves to. Nothing here resolves it — EXC-379/386 render the workload and
// read the address then.
type ResolvedReference struct {
	Name   string
	Target ReferenceTarget
	Scope  ReferenceScope
}

// EnvVar is one environment variable: a literal value, a reference to another
// source in the project, or a secret. Exactly one payload is set, and it is
// the one Kind names. Value is a pointer so the empty string, a legitimate
// value, is distinguishable from an absent one.
type EnvVar struct {
	Name      string           `json:"name"`
	Kind      VarKind          `json:"kind"`
	Value     *string          `json:"value,omitempty"`
	Reference *ReferenceTarget `json:"reference,omitempty"`
	Secret    *SecretRef       `json:"secret,omitempty"`
}

// App is one customer application within a project.
type App struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	// Name is unique per project and is what a human calls the app.
	Name string `json:"name"`
	// Image is the registry reference exactly as the customer gave it.
	Image string `json:"image"`
	// ResolvedDigest is the digest a deploy resolved Image to. Empty until
	// EXC-386 fills it; nothing here resolves anything.
	ResolvedDigest string `json:"resolvedDigest,omitempty"`
	// Env is ordered, so what the renderer emits matches what was authored.
	Env []EnvVar `json:"env"`
	// Port is the single HTTP port the container exposes.
	Port int `json:"port"`
	// HealthCheckPath is optional and must start with '/' when set.
	HealthCheckPath string `json:"healthCheckPath,omitempty"`
	// Replicas is 0-3; zero means intentionally stopped.
	Replicas int `json:"replicas"`
	// Tier resolves to cpu/memory through config.GetAppTierConfig.
	Tier   domain.TierType `json:"tier"`
	Status string          `json:"status"`
	// Version increments on every stored change, as the edge-function record
	// does, so a caller can tell one revision of the record from another.
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

var (
	// validName — the per-project app name: lowercase, hyphen-separated, and
	// short enough to survive being part of a DNS label downstream.
	validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,49}$`)
	// validID — the generated app id, safe in a URL path.
	validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	// validProjectID matches the ids the platform generates and the ones
	// self-hosted installs use.
	validProjectID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	// validEnvName — POSIX-ish environment variable names.
	validEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// validSourceName — the name of a source within the project (a database
	// name), as the platform recorded it.
	validSourceName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// validSecretKey — a field name inside a vault secret.
	validSecretKey = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	// validHealthPath — an absolute path with no query, fragment or space.
	validHealthPath = regexp.MustCompile(`^/[A-Za-z0-9._~/%:@!$&'()*+,;=-]*$`)
)

// Image-reference grammar, following the distribution spec closely enough that
// anything accepted here is something a registry can parse.
var (
	// imageDigest — "<algorithm>:<hex>", e.g. sha256:<64 hex>.
	imageDigest = regexp.MustCompile(`^[a-z0-9]+(?:[.+_-][a-z0-9]+)*:[a-fA-F0-9]{32,}$`)
	// imageTag — the reference after ':'.
	imageTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	// imagePathComponent — one lowercase repository segment.
	imagePathComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	// imageHost — a registry host with an optional port.
	imageHost = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*(?::[0-9]{1,5})?$`)
)

// ErrInvalidImage marks a reference no registry could resolve.
var ErrInvalidImage = errors.New("invalid image reference")

// ValidateImageReference checks that ref is a well-formed registry reference
// carrying an explicit tag or digest.
//
// A bare "nginx" is refused rather than read as "nginx:latest": defaulting the
// tag would pick a value the customer never gave, and which image ran would
// depend on when the deploy happened.
func ValidateImageReference(ref string) error {
	if ref == "" {
		return fmt.Errorf("%w: image is required", ErrInvalidImage)
	}
	if len(ref) > MaxImageRefLength {
		return fmt.Errorf("%w: exceeds %d characters", ErrInvalidImage, MaxImageRefLength)
	}
	if strings.TrimSpace(ref) != ref || strings.ContainsAny(ref, " \t\r\n") {
		return fmt.Errorf("%w: must not contain whitespace", ErrInvalidImage)
	}

	name := ref
	if at := strings.Index(ref, "@"); at >= 0 {
		name = ref[:at]
		if !imageDigest.MatchString(ref[at+1:]) {
			return fmt.Errorf("%w: digest must be <algorithm>:<hex>", ErrInvalidImage)
		}
	} else {
		lastSlash := strings.LastIndex(ref, "/")
		colon := strings.LastIndex(ref, ":")
		if colon < 0 || colon < lastSlash {
			return fmt.Errorf("%w: must name an explicit tag or digest", ErrInvalidImage)
		}
		tag := ref[colon+1:]
		if len(tag) > maxImageTagLength || !imageTag.MatchString(tag) {
			return fmt.Errorf("%w: invalid tag", ErrInvalidImage)
		}
		name = ref[:colon]
	}
	return validateImageName(name)
}

// validateImageName checks the repository part: an optional registry host
// followed by one or more lowercase path components.
func validateImageName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: repository is required", ErrInvalidImage)
	}
	parts := strings.Split(name, "/")
	if isRegistryHost(parts[0]) && len(parts) > 1 {
		if !imageHost.MatchString(parts[0]) {
			return fmt.Errorf("%w: invalid registry host", ErrInvalidImage)
		}
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return fmt.Errorf("%w: repository is required", ErrInvalidImage)
	}
	for _, part := range parts {
		if !imagePathComponent.MatchString(part) {
			return fmt.Errorf("%w: invalid repository component %q", ErrInvalidImage, part)
		}
	}
	return nil
}

// isRegistryHost reports whether the first component names a registry rather
// than a repository namespace — the spec's rule: it has a dot, a port, or is
// literally localhost.
func isRegistryHost(part string) bool {
	return strings.ContainsAny(part, ".:") || part == "localhost"
}

// Validate checks every field of the app at the boundary. Nothing is
// defaulted or corrected: an unparseable image reference or an unknown tier is
// a refusal.
func (a *App) Validate() error {
	if !validID.MatchString(a.ID) {
		return errors.New("invalid app id")
	}
	if !validProjectID.MatchString(a.ProjectID) {
		return errors.New("invalid project id")
	}
	if err := ValidateName(a.Name); err != nil {
		return err
	}
	if err := ValidateImageReference(a.Image); err != nil {
		return err
	}
	if a.Port < 1 || a.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if err := validateHealthCheckPath(a.HealthCheckPath); err != nil {
		return err
	}
	if err := a.validateReplicas(); err != nil {
		return err
	}
	if err := validateEnv(a.ProjectID, a.Env); err != nil {
		return err
	}
	if !validStatuses[a.Status] {
		return fmt.Errorf("unknown app status: %q", a.Status)
	}
	return nil
}

// validateReplicas holds the replica count to both caps: the platform-wide
// one and the tier's. The tier lookup doubles as the tier check, so an unknown
// tier is refused here rather than defaulted downstream.
func (a *App) validateReplicas() error {
	if a.Replicas < 0 || a.Replicas > MaxReplicas {
		return fmt.Errorf("replicas must be between 0 and %d", MaxReplicas)
	}
	tier, err := config.GetAppTierConfig(a.Tier)
	if err != nil {
		return err
	}
	if a.Replicas > tier.MaxReplicas {
		return fmt.Errorf("tier %s allows at most %d replicas", a.Tier, tier.MaxReplicas)
	}
	return nil
}

// ValidateName checks the per-project app name.
func ValidateName(name string) error {
	if len(name) < minNameLength || len(name) > MaxNameLength || !validName.MatchString(name) {
		return fmt.Errorf("invalid app name: must be %d-%d lowercase alphanumeric or hyphen characters, starting with a letter or digit",
			minNameLength, MaxNameLength)
	}
	return nil
}

// ValidateID checks an app id taken from a URL path.
func ValidateID(id string) error {
	if !validID.MatchString(id) {
		return errors.New("invalid app id")
	}
	return nil
}

// ValidateProjectID checks a project id taken from a URL path.
func ValidateProjectID(projectID string) error {
	if !validProjectID.MatchString(projectID) {
		return errors.New("invalid project id")
	}
	return nil
}

func validateHealthCheckPath(path string) error {
	if path == "" {
		return nil
	}
	if len(path) > MaxHealthCheckPathLength || !validHealthPath.MatchString(path) {
		return errors.New("health check path must start with '/' and contain no whitespace, query or fragment")
	}
	return nil
}

// validateEnv checks the variables as a set: the set fits its caps, names are
// unique, and every variable carries exactly the payload its kind names.
func validateEnv(projectID string, env []EnvVar) error {
	if len(env) > MaxEnvVars {
		return fmt.Errorf("at most %d environment variables are allowed", MaxEnvVars)
	}
	seen := make(map[string]bool, len(env))
	total := 0
	for _, v := range env {
		if len(v.Name) > MaxEnvNameLength || !validEnvName.MatchString(v.Name) {
			return fmt.Errorf("invalid environment variable name: %q", v.Name)
		}
		if seen[v.Name] {
			return fmt.Errorf("duplicate environment variable: %q", v.Name)
		}
		seen[v.Name] = true
		if err := validateVarPayload(projectID, v); err != nil {
			return err
		}
		total += envVarBytes(v)
		if total > MaxTotalEnvBytes {
			return fmt.Errorf("the environment variables exceed %d bytes in total", MaxTotalEnvBytes)
		}
	}
	return nil
}

// validateVarPayload checks that exactly the payload the kind names is set.
// The kind is authoritative: a variable declared a secret that also carries a
// literal is a refusal, not a choice to be made downstream.
func validateVarPayload(projectID string, v EnvVar) error {
	present := 0
	for _, set := range []bool{v.Value != nil, v.Reference != nil, v.Secret != nil} {
		if set {
			present++
		}
	}
	if present != 1 {
		return fmt.Errorf("environment variable %q must carry exactly one of a value, a reference or a secret", v.Name)
	}

	switch v.Kind {
	case KindLiteral:
		if v.Value == nil {
			return fmt.Errorf("environment variable %q is declared %s and must carry a value", v.Name, v.Kind)
		}
		if len(*v.Value) > MaxLiteralValueLength {
			return fmt.Errorf("environment variable %q exceeds %d bytes", v.Name, MaxLiteralValueLength)
		}
		return nil
	case KindReference:
		if v.Reference == nil {
			return fmt.Errorf("environment variable %q is declared %s and must carry a reference", v.Name, v.Kind)
		}
		if err := validateReferenceTarget(*v.Reference); err != nil {
			return fmt.Errorf("environment variable %q: %w", v.Name, err)
		}
		return nil
	case KindSecret:
		if v.Secret == nil {
			return fmt.Errorf("environment variable %q is declared %s and must carry a secret", v.Name, v.Kind)
		}
		if err := validateSecretRef(projectID, *v.Secret); err != nil {
			return fmt.Errorf("environment variable %q: %w", v.Name, err)
		}
		return nil
	default:
		return fmt.Errorf("environment variable %q has an unknown kind: %q (expected %s, %s or %s)",
			v.Name, v.Kind, KindLiteral, KindReference, KindSecret)
	}
}

// envVarBytes is what one variable costs against the total cap: the name plus
// whatever names its value.
func envVarBytes(v EnvVar) int {
	size := len(v.Name)
	switch {
	case v.Value != nil:
		size += len(*v.Value)
	case v.Reference != nil:
		size += len(v.Reference.SourceKind) + len(v.Reference.SourceName) + len(v.Reference.Variable)
	case v.Secret != nil:
		size += len(v.Secret.Path) + len(v.Secret.Key)
	}
	return size
}

// validateReferenceTarget checks a reference's shape: a known source kind, a
// plausible source name, and a variable that source actually exposes. Whether
// the named source exists in the project is decided by ValidateReferences,
// which is the only part that needs to know the project.
func validateReferenceTarget(target ReferenceTarget) error {
	if target.SourceKind != SourceDatabase {
		return fmt.Errorf("unknown reference source kind: %q (expected %s)", target.SourceKind, SourceDatabase)
	}
	if !validSourceName.MatchString(target.SourceName) {
		return fmt.Errorf("invalid reference source name: %q", target.SourceName)
	}
	for _, known := range databaseSourceVariables {
		if target.Variable == known {
			return nil
		}
	}
	return fmt.Errorf("a %s source exposes no variable %q (it exposes %s)",
		target.SourceKind, target.Variable, strings.Join(databaseSourceVariables, ", "))
}

// ErrUnresolvedReference marks a reference to a source the project does not
// have. It is fatal by design: the alternative is an app that deploys with an
// empty DATABASE_URL and fails somewhere the customer cannot see.
var ErrUnresolvedReference = errors.New("unresolved reference")

// SourceLookup answers whether a project exposes a named source. It is how
// ValidateReferences learns that a reference can be honoured; nothing here
// connects to the source, reads it, or resolves an address from it.
type SourceLookup interface {
	HasSource(projectID string, kind SourceKind, name string) (bool, error)
}

// ValidateReferences refuses the app unless every reference it declares names
// a source the project actually has.
//
// Nothing is injected automatically and no name is reserved: a project's
// database reaches an app only because the customer declared a reference to
// it, and a project with no database simply has nothing to reference.
func ValidateReferences(app *App, sources SourceLookup) error {
	for _, v := range app.Env {
		if v.Kind != KindReference || v.Reference == nil {
			continue
		}
		exists, err := sources.HasSource(app.ProjectID, v.Reference.SourceKind, v.Reference.SourceName)
		if err != nil {
			return fmt.Errorf("resolve reference for %q: %w", v.Name, err)
		}
		if !exists {
			return fmt.Errorf("%w: %q points at %s, which this project does not have",
				ErrUnresolvedReference, v.Name, v.Reference)
		}
	}
	return nil
}

// Resolutions lists what a renderer must resolve for this app: one entry per
// reference variable, each carrying the address scope it resolves to.
func (a *App) Resolutions() []ResolvedReference {
	out := make([]ResolvedReference, 0, len(a.Env))
	for _, v := range a.Env {
		if v.Kind != KindReference || v.Reference == nil {
			continue
		}
		out = append(out, ResolvedReference{Name: v.Name, Target: *v.Reference, Scope: ScopeInternal})
	}
	return out
}

// validateSecretRef holds a secret reference inside the project's own vault
// prefix. Without the prefix check, an app could name another tenant's secret
// and have the deploy read it on its behalf.
func validateSecretRef(projectID string, ref SecretRef) error {
	prefix := "projects/" + projectID + "/"
	if !strings.HasPrefix(ref.Path, prefix) {
		return fmt.Errorf("secret path must start with %q", prefix)
	}
	for _, segment := range strings.Split(ref.Path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid secret path segment: %q", segment)
		}
	}
	if !validSecretKey.MatchString(ref.Key) {
		return fmt.Errorf("invalid secret key: %q", ref.Key)
	}
	return nil
}
