package k8s

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// updateGolden is a flag, not an env var, so regenerating (`-update-golden`) is deliberate and diffed before commit.
var updateGolden = flag.Bool("update-golden", false, "rewrite the app workload golden manifests")

// fakeResolver stands in for the deploy-time resolver (EXC-386): an unknown
// reference or secret is an error, never an empty string.
type fakeResolver struct {
	namespace string
	// seenScopes proves the renderer never asks for a public address.
	seenScopes []apphost.ReferenceScope
	refErr     error
	// literalFor proves the renderer refuses a credential in the clear.
	literalFor map[string]string
}

func (f *fakeResolver) ResolveReference(projectID string, ref apphost.ResolvedReference) (EnvSource, error) {
	f.seenScopes = append(f.seenScopes, ref.Scope)
	if f.refErr != nil {
		return EnvSource{}, f.refErr
	}
	if value, ok := f.literalFor[ref.Name]; ok {
		return EnvSource{Literal: &value}, nil
	}
	switch ref.Target.Variable {
	case "PGHOST":
		host := ref.Target.SourceName + "-postgres-rw." + f.namespace + ".svc.cluster.local"
		return EnvSource{Literal: &host}, nil
	case "PGPORT":
		port := "5432"
		return EnvSource{Literal: &port}, nil
	case "PGDATABASE":
		name := "app"
		return EnvSource{Literal: &name}, nil
	case "PGUSER":
		user := "excalibase_app"
		return EnvSource{Literal: &user}, nil
	case "PGPASSWORD", "DATABASE_URL":
		return EnvSource{Secret: &SecretKeySelector{
			SecretName: projectID + "-app-credentials",
			Key:        ref.Target.Variable,
		}}, nil
	}
	return EnvSource{}, errUnexpectedReference
}

func (f *fakeResolver) ResolveSecret(projectID string, ref apphost.SecretRef) (SecretKeySelector, error) {
	if !strings.HasPrefix(ref.Path, "projects/"+projectID+"/") {
		return SecretKeySelector{}, errUnexpectedReference
	}
	return SecretKeySelector{SecretName: projectID + "-app-secrets", Key: ref.Key}, nil
}

var errUnexpectedReference = &resolverError{"the test resolver was asked for something it does not have"}

type resolverError struct{ msg string }

func (e *resolverError) Error() string { return e.msg }

func literal(v string) *string { return &v }

// minimalApp is a valid app with nothing optional set.
func minimalApp() *apphost.App {
	return &apphost.App{
		ID:        "app-01H",
		ProjectID: "proj-abc",
		Name:      "web",
		Image:     "ghcr.io/acme/web:1.4.2",
		Port:      8080,
		Replicas:  1,
		Tier:      domain.Free,
		Status:    apphost.StatusCreated,
	}
}

// fullApp exercises every env kind, a health check and the replica ceiling.
func fullApp() *apphost.App {
	app := minimalApp()
	app.Tier = domain.Standard
	app.Replicas = 3
	app.HealthCheckPath = "/healthz"
	app.Env = []apphost.EnvVar{
		{Name: "LOG_LEVEL", Kind: apphost.KindLiteral, Value: literal("debug")},
		{Name: "EMPTY", Kind: apphost.KindLiteral, Value: literal("")},
		{Name: "DATABASE_URL", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
			SourceKind: apphost.SourceDatabase, SourceName: "proj-abc", Variable: "DATABASE_URL",
		}},
		{Name: "PGHOST", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
			SourceKind: apphost.SourceDatabase, SourceName: "proj-abc", Variable: "PGHOST",
		}},
		{Name: "STRIPE_KEY", Kind: apphost.KindSecret, Secret: &apphost.SecretRef{
			Path: "projects/proj-abc/stripe", Key: "api_key",
		}},
	}
	return app
}

const testNamespace = "org1-proj-abc"

func newResolver() *fakeResolver { return &fakeResolver{namespace: testNamespace} }

func mustRender(t *testing.T, app *apphost.App, resolver Resolver) *AppWorkload {
	t.Helper()
	workload, err := RenderAppWorkload(testNamespace, app, resolver)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return workload
}

func TestRenderAppWorkloadUsesProjectNamespaceAndOwnershipLabels(t *testing.T) {
	workload := mustRender(t, fullApp(), newResolver())

	for name, got := range map[string]string{
		"deployment":     workload.Deployment.Namespace,
		"network policy": workload.NetworkPolicy.Namespace,
	} {
		if got != testNamespace {
			t.Errorf("%s namespace = %q, want %q", name, got, testNamespace)
		}
	}
	for _, want := range []struct{ key, value string }{
		{"excalibase.io/component", appComponentLabel},
		{"excalibase.io/app", "app-01H"},
		{"excalibase.io/project", "proj-abc"},
		{"excalibase.io/tier", "standard"},
		{"app.kubernetes.io/name", "web"},
		{"app.kubernetes.io/managed-by", appManagedByValue},
	} {
		if got := workload.Deployment.Labels[want.key]; got != want.value {
			t.Errorf("deployment label %s = %q, want %q", want.key, got, want.value)
		}
	}
	if got := workload.NetworkPolicy.Labels["excalibase.io/app"]; got != "app-01H" {
		t.Errorf("policy must carry the app label, got %q", got)
	}
}

// The selector is the app's immutable identity and must not carry the tier: a
// Deployment's selector cannot be changed after creation, so a tier upgrade
// would leave the workload permanently un-updatable. The Deno renderer puts
// the tier in its selector; this one must not.
func TestRenderAppWorkloadSelectorExcludesMutableLabels(t *testing.T) {
	workload := mustRender(t, fullApp(), newResolver())
	selector := workload.Deployment.Spec.Selector.MatchLabels

	if _, ok := selector["excalibase.io/tier"]; ok {
		t.Error("the tier must not be part of the immutable selector")
	}
	if selector["excalibase.io/app"] != "app-01H" {
		t.Errorf("selector must identify the app, got %v", selector)
	}
	for key, value := range selector {
		if workload.Deployment.Spec.Template.Labels[key] != value {
			t.Errorf("pod template must carry selector label %s=%s", key, value)
		}
	}
}

func TestRenderAppWorkloadResourcesPerTier(t *testing.T) {
	for _, tier := range []domain.TierType{domain.Free, domain.Standard, domain.Enterprise} {
		t.Run(string(tier), func(t *testing.T) {
			want, err := config.GetAppTierConfig(tier)
			if err != nil {
				t.Fatalf("catalogue: %v", err)
			}
			app := minimalApp()
			app.Tier = tier
			workload := mustRender(t, app, newResolver())

			got := workload.Deployment.Spec.Template.Spec.Containers[0].Resources
			for _, check := range []struct {
				what string
				got  string
				want string
			}{
				{"cpu request", got.Requests.Cpu().String(), want.CPURequest},
				{"cpu limit", got.Limits.Cpu().String(), want.CPULimit},
				{"memory request", got.Requests.Memory().String(), want.MemoryRequest},
				{"memory limit", got.Limits.Memory().String(), want.MemoryLimit},
			} {
				if check.got != check.want {
					t.Errorf("%s = %s, want %s", check.what, check.got, check.want)
				}
			}
		})
	}
}

// A tier with no mapping is a refusal, not a default.
func TestRenderAppWorkloadRefusesUnknownTier(t *testing.T) {
	for _, tier := range []domain.TierType{"", "PLATINUM", "free"} {
		app := minimalApp()
		app.Tier = tier
		if _, err := RenderAppWorkload(testNamespace, app, newResolver()); err == nil {
			t.Errorf("tier %q must be refused", tier)
		}
	}
}

// A literal becomes a plain value; the empty string is a value, not an absence.
func TestRenderAppWorkloadLiteralEnv(t *testing.T) {
	workload := mustRender(t, fullApp(), newResolver())
	env := workload.Deployment.Spec.Template.Spec.Containers[0].Env

	got := map[string]corev1.EnvVar{}
	for _, e := range env {
		got[e.Name] = e
	}
	if got["LOG_LEVEL"].Value != "debug" || got["LOG_LEVEL"].ValueFrom != nil {
		t.Errorf("LOG_LEVEL = %+v, want the plain value", got["LOG_LEVEL"])
	}
	if _, ok := got["EMPTY"]; !ok {
		t.Error("an empty literal must still be rendered")
	}
	if got["EMPTY"].Value != "" || got["EMPTY"].ValueFrom != nil {
		t.Errorf("EMPTY = %+v, want the empty value", got["EMPTY"])
	}
}

// A secret becomes a secretKeyRef. The value must never reach the manifest.
func TestRenderAppWorkloadSecretIsNeverInlined(t *testing.T) {
	workload := mustRender(t, fullApp(), newResolver())
	for _, e := range workload.Deployment.Spec.Template.Spec.Containers[0].Env {
		if e.Name != "STRIPE_KEY" {
			continue
		}
		if e.Value != "" {
			t.Fatalf("STRIPE_KEY carries an inline value %q", e.Value)
		}
		ref := e.ValueFrom.SecretKeyRef
		if ref.Name != "proj-abc-app-secrets" || ref.Key != "api_key" {
			t.Fatalf("STRIPE_KEY secretKeyRef = %+v", ref)
		}
		return
	}
	t.Fatal("STRIPE_KEY was not rendered")
}

// A reference resolves at the internal cluster scope. The public per-tenant
// endpoint is a different address for the same database and an app running
// beside it has no reason to leave the cluster.
func TestRenderAppWorkloadReferenceResolvesInternally(t *testing.T) {
	resolver := newResolver()
	workload := mustRender(t, fullApp(), resolver)

	if len(resolver.seenScopes) != 2 {
		t.Fatalf("expected both references to be resolved, got %d", len(resolver.seenScopes))
	}
	for _, scope := range resolver.seenScopes {
		if scope != apphost.ScopeInternal {
			t.Errorf("reference resolved at scope %q, want %q", scope, apphost.ScopeInternal)
		}
	}
	for _, e := range workload.Deployment.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "PGHOST" {
			want := "proj-abc-postgres-rw." + testNamespace + ".svc.cluster.local"
			if e.Value != want {
				t.Errorf("PGHOST = %q, want the internal address %q", e.Value, want)
			}
		}
	}
}

// A reference that cannot resolve is fatal at render time, not an empty string
// in a running container.
func TestRenderAppWorkloadRefusesUnresolvableReference(t *testing.T) {
	resolver := newResolver()
	resolver.refErr = errUnexpectedReference
	if _, err := RenderAppWorkload(testNamespace, fullApp(), resolver); err == nil {
		t.Fatal("an unresolvable reference must be refused")
	}
	if _, err := RenderAppWorkload(testNamespace, fullApp(), nil); err == nil {
		t.Fatal("an app with references and no resolver must be refused")
	}
}

// A credential-bearing reference must come back as a secret. A resolver that
// hands one over in the clear is refused rather than written into a Deployment
// any namespace reader can dump.
func TestRenderAppWorkloadRefusesCredentialInTheClear(t *testing.T) {
	for _, variable := range []string{"DATABASE_URL", "PGPASSWORD"} {
		app := minimalApp()
		app.Env = []apphost.EnvVar{{
			Name: variable, Kind: apphost.KindReference,
			Reference: &apphost.ReferenceTarget{
				SourceKind: apphost.SourceDatabase, SourceName: "proj-abc", Variable: variable,
			},
		}}
		resolver := newResolver()
		resolver.literalFor = map[string]string{variable: "postgres://u:p@h/db"}
		if _, err := RenderAppWorkload(testNamespace, app, resolver); err == nil {
			t.Errorf("%s handed over as a literal must be refused", variable)
		}
	}
}

// Zero replicas is a deliberate state and must be rendered, not omitted: an
// absent replica count means one, which would restart an app a tenant stopped.
func TestRenderAppWorkloadRendersZeroReplicas(t *testing.T) {
	app := minimalApp()
	app.Replicas = 0
	app.Status = apphost.StatusStopped
	workload := mustRender(t, app, newResolver())

	if workload.Deployment.Spec.Replicas == nil {
		t.Fatal("replicas must be set explicitly, not left nil")
	}
	if *workload.Deployment.Spec.Replicas != 0 {
		t.Fatalf("replicas = %d, want 0", *workload.Deployment.Spec.Replicas)
	}
}

func TestRenderAppWorkloadPortAndProbe(t *testing.T) {
	withProbe := mustRender(t, fullApp(), newResolver())
	container := withProbe.Deployment.Spec.Template.Spec.Containers[0]
	if container.Ports[0].ContainerPort != 8080 {
		t.Errorf("container port = %d, want 8080", container.Ports[0].ContainerPort)
	}
	probe := container.ReadinessProbe
	if probe == nil || probe.HTTPGet == nil {
		t.Fatal("a health check path must render a readiness probe")
	}
	if probe.HTTPGet.Path != "/healthz" || probe.HTTPGet.Port.IntValue() != 8080 {
		t.Errorf("probe = %+v, want /healthz on 8080", probe.HTTPGet)
	}

	noProbe := mustRender(t, minimalApp(), newResolver())
	if noProbe.Deployment.Spec.Template.Spec.Containers[0].ReadinessProbe != nil {
		t.Error("an app with no health check must render no readiness probe")
	}
}

// The image is used exactly as recorded, and a mutable tag is always pulled:
// a node that cached a different build under the same tag must not serve it.
func TestRenderAppWorkloadImageAndPullPolicy(t *testing.T) {
	tagged := mustRender(t, minimalApp(), newResolver())
	container := tagged.Deployment.Spec.Template.Spec.Containers[0]
	if container.Image != "ghcr.io/acme/web:1.4.2" {
		t.Errorf("image = %q, want it verbatim", container.Image)
	}
	if container.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("a tag must be pulled always, got %q", container.ImagePullPolicy)
	}

	app := minimalApp()
	app.Image = "ghcr.io/acme/web@sha256:" + strings.Repeat("a", 64)
	digested := mustRender(t, app, newResolver())
	if got := digested.Deployment.Spec.Template.Spec.Containers[0].ImagePullPolicy; got != corev1.PullIfNotPresent {
		t.Errorf("a digest is immutable and need not be re-pulled, got %q", got)
	}
}

// Customer code gets no Kubernetes API token: a container that escapes must
// not find credentials to the cluster mounted beside it.
func TestRenderAppWorkloadMountsNoServiceAccountToken(t *testing.T) {
	spec := mustRender(t, fullApp(), newResolver()).Deployment.Spec.Template.Spec
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Error("the service account token must be explicitly not mounted")
	}
}

// Refused rather than rendered into something that fails obscurely in the cluster.
func TestRenderAppWorkloadRefusesInvalidInput(t *testing.T) {
	cases := map[string]func(*apphost.App){
		"no image":        func(a *apphost.App) { a.Image = "" },
		"implicit latest": func(a *apphost.App) { a.Image = "nginx" },
		"bad port":        func(a *apphost.App) { a.Port = 0 },
		"too many replicas": func(a *apphost.App) {
			a.Tier = domain.Free
			a.Replicas = 3
		},
		"oversized id": func(a *apphost.App) { a.ID = strings.Repeat("a", 64) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			app := minimalApp()
			mutate(app)
			if _, err := RenderAppWorkload(testNamespace, app, newResolver()); err == nil {
				t.Error("must be refused")
			}
		})
	}
	if _, err := RenderAppWorkload("", minimalApp(), newResolver()); err == nil {
		t.Error("an empty namespace must be refused")
	}
	if _, err := RenderAppWorkload(testNamespace, nil, newResolver()); err == nil {
		t.Error("a nil app must be refused")
	}
}

func TestRenderAppWorkloadEgressPolicy(t *testing.T) {
	policy := mustRender(t, fullApp(), newResolver()).NetworkPolicy

	if len(policy.Spec.PolicyTypes) != 1 || policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Fatalf("the policy must be egress-only, got %v", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 2 {
		t.Fatalf("expected a DNS rule and a database rule, got %d", len(policy.Spec.Egress))
	}
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				t.Errorf("no rule may open an address range to customer code: %v", peer.IPBlock)
			}
			if peer.NamespaceSelector == nil {
				continue
			}
			name := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]
			if name != "kube-system" {
				t.Errorf("customer code may not reach namespace %q", name)
			}
		}
	}
	// A pod selector with no namespace selector cannot cross into another tenant.
	dbRule := policy.Spec.Egress[1]
	if dbRule.To[0].NamespaceSelector != nil || dbRule.To[0].PodSelector == nil {
		t.Errorf("the database rule must be confined to this namespace, got %+v", dbRule.To[0])
	}
	if dbRule.Ports[0].Port.IntValue() != 5432 {
		t.Errorf("the database rule must open 5432 only, got %v", dbRule.Ports[0].Port)
	}
}

func TestRenderAppWorkloadEgressPolicyWithoutDatabase(t *testing.T) {
	policy := mustRender(t, minimalApp(), newResolver()).NetworkPolicy
	if len(policy.Spec.Egress) != 1 {
		t.Fatalf("an app with no reference needs DNS only, got %d rules", len(policy.Spec.Egress))
	}
}

// The same app renders byte-identical manifests every time, so a later
// reconcile can compare what it finds against what it wants.
func TestRenderAppWorkloadIsDeterministic(t *testing.T) {
	first := renderYAML(t, fullApp())
	for i := 0; i < 20; i++ {
		if got := renderYAML(t, fullApp()); got != first {
			t.Fatalf("render %d differs from the first", i)
		}
	}
}

// A changed app changes the pod template, so the Deployment rolls rather than
// keeping the old pods alive under a new record.
func TestRenderAppWorkloadConfigHashTracksTheApp(t *testing.T) {
	base := mustRender(t, fullApp(), newResolver())
	hash := base.Deployment.Spec.Template.Annotations[appConfigHashAnnotation]
	if hash == "" {
		t.Fatal("the pod template must carry a config hash")
	}
	changed := fullApp()
	changed.Image = "ghcr.io/acme/web:1.4.3"
	if got := mustRender(t, changed, newResolver()).Deployment.Spec.Template.Annotations[appConfigHashAnnotation]; got == hash {
		t.Error("a changed image must change the config hash")
	}
}

func renderYAML(t *testing.T, app *apphost.App) string {
	t.Helper()
	workload := mustRender(t, app, newResolver())
	deployment := workload.Deployment.DeepCopy()
	deployment.TypeMeta = metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment"}
	policy := workload.NetworkPolicy.DeepCopy()
	policy.TypeMeta = metav1.TypeMeta{APIVersion: networkingv1.SchemeGroupVersion.String(), Kind: "NetworkPolicy"}

	var out strings.Builder
	for _, obj := range []interface{}{deployment, policy} {
		encoded, err := yaml.Marshal(obj)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		out.WriteString("---\n")
		out.Write(encoded)
	}
	return out.String()
}

// Rewritten only under -update-golden, so a rendered-output change is read before it is accepted.
func TestAppWorkloadGoldenManifests(t *testing.T) {
	stopped := fullApp()
	stopped.Replicas = 0
	stopped.Status = apphost.StatusStopped

	enterprise := minimalApp()
	enterprise.Tier = domain.Enterprise
	enterprise.Replicas = 2

	cases := map[string]*apphost.App{
		"minimal-free":      minimalApp(),
		"full-standard":     fullApp(),
		"stopped":           stopped,
		"enterprise-no-env": enterprise,
	}
	for name, app := range cases {
		t.Run(name, func(t *testing.T) {
			got := renderYAML(t, app)
			path := filepath.Join("testdata", "app_workload", name+".yaml")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("create golden dir: %v", err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (regenerate with -update-golden): %v", err)
			}
			if got != string(want) {
				t.Errorf("rendered manifests differ from %s\n--- got ---\n%s", path, got)
			}
		})
	}
}

// These paths are unreachable through a validated record; exercised directly
// as the guard against tenant-supplied data panicking the control plane.
func TestRenderEnvVarRefusals(t *testing.T) {
	app := minimalApp()
	target := &apphost.ReferenceTarget{
		SourceKind: apphost.SourceDatabase, SourceName: "proj-abc", Variable: "PGHOST",
	}
	cases := map[string]struct {
		declared apphost.EnvVar
		resolver Resolver
	}{
		"unknown kind": {
			declared: apphost.EnvVar{Name: "X", Kind: "magic", Value: literal("v")},
			resolver: newResolver(),
		},
		"literal with no value": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindLiteral},
			resolver: newResolver(),
		},
		"secret with no payload": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindSecret},
			resolver: newResolver(),
		},
		"reference with no payload": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindReference},
			resolver: newResolver(),
		},
		"secret the resolver does not have": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindSecret, Secret: &apphost.SecretRef{
				Path: "projects/other-tenant/stripe", Key: "api_key",
			}},
			resolver: newResolver(),
		},
		"secret resolved to an incomplete selector": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindSecret, Secret: &apphost.SecretRef{
				Path: "projects/proj-abc/stripe", Key: "api_key",
			}},
			resolver: &brokenResolver{},
		},
		"reference resolved to nothing": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindReference, Reference: target},
			resolver: &brokenResolver{},
		},
		"reference resolved to both a value and a secret": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindReference, Reference: target},
			resolver: &brokenResolver{both: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := renderEnvVar(app, tc.declared, tc.resolver); err == nil {
				t.Error("must be refused")
			}
		})
	}
}

// brokenResolver answers with sources that are not usable, standing in for a
// resolver implementation that goes wrong.
type brokenResolver struct{ both bool }

func (b *brokenResolver) ResolveReference(string, apphost.ResolvedReference) (EnvSource, error) {
	if b.both {
		value := "v"
		return EnvSource{Literal: &value, Secret: &SecretKeySelector{SecretName: "s", Key: "k"}}, nil
	}
	return EnvSource{}, nil
}

func (b *brokenResolver) ResolveSecret(string, apphost.SecretRef) (SecretKeySelector, error) {
	return SecretKeySelector{SecretName: "s"}, nil
}

// A tier whose catalogue entry is unparseable is fatal, never a pod with no
// limits at all.
func TestAppResourceRequirementsRefusesUnparseableCatalogue(t *testing.T) {
	_, err := appResourceRequirements(config.AppTierConfig{
		CPURequest: "50m", CPULimit: "not-a-quantity", MemoryRequest: "128Mi", MemoryLimit: "256Mi",
	})
	if err == nil {
		t.Error("an unparseable quantity must be refused")
	}
}
