package k8s

import (
	"errors"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// updateGolden is a flag, not an env var, so regenerating (`-update-golden`) is deliberate and diffed before commit.
var updateGolden = flag.Bool("update-golden", false, "rewrite the app workload golden manifests")

// fakeResolver stands in for the deploy-time resolver: an unknown reference
// or secret is an error, never an empty string.
type fakeResolver struct {
	namespace string
	// seenScopes proves the renderer never asks for a public address.
	seenScopes []apphost.ReferenceScope
	refErr     error
	// valueFor overrides what a variable resolves to.
	valueFor map[string]string
}

const (
	testDatabaseURL = "postgres://owner:owner-pass-9f2@proj-abc-postgres-rw.org1-proj-abc.svc.cluster.local:5432/app?sslmode=require"
	testStripeKey   = "sk_test_stripe_value_31c"
)

func (f *fakeResolver) ResolveReference(projectID string, ref apphost.ResolvedReference) (string, error) {
	f.seenScopes = append(f.seenScopes, ref.Scope)
	if f.refErr != nil {
		return "", f.refErr
	}
	if value, ok := f.valueFor[ref.Name]; ok {
		return value, nil
	}
	switch ref.Target.Variable {
	case "PGHOST":
		return ref.Target.SourceName + "-postgres-rw." + f.namespace + ".svc.cluster.local", nil
	case "PGPORT":
		return "5432", nil
	case "PGDATABASE":
		return "app", nil
	case "PGUSER":
		return "owner", nil
	case "PGPASSWORD":
		return "owner-pass-9f2", nil
	case "DATABASE_URL":
		return testDatabaseURL, nil
	}
	return "", errUnexpectedReference
}

func (f *fakeResolver) ResolveSecret(projectID string, ref apphost.SecretRef) (string, error) {
	if !strings.HasPrefix(ref.Path, "projects/"+projectID+"/") {
		return "", errUnexpectedReference
	}
	if value, ok := f.valueFor[ref.Path]; ok {
		return value, nil
	}
	return testStripeKey, nil
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
		{Name: "STRIPE_KEY", Kind: apphost.KindSecret, Secret: ownSecret(app, "STRIPE_KEY")},
	}
	return app
}

func ownSecret(app *apphost.App, name string) *apphost.SecretRef {
	ref := apphost.AppSecretRef(app.ProjectID, app.ID, name)
	return &ref
}

const (
	testNamespace    = "org1-proj-abc"
	testRuntimeClass = "gvisor"
)

var testRoute = AppRouteOptions{
	Domain:               "apps.example.com",
	IngressClass:         "haproxy",
	IngressFromNamespace: "haproxy-controller",
}

var testRenderOptions = AppRenderOptions{RuntimeClass: testRuntimeClass, Route: testRoute, EnvRevision: "7"}

func newResolver() *fakeResolver { return &fakeResolver{namespace: testNamespace} }

func mustRender(t *testing.T, app *apphost.App, resolver Resolver) *AppWorkload {
	t.Helper()
	workload, err := RenderAppWorkload(testNamespace, app, resolver, testRenderOptions)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return workload
}

func TestRenderAppWorkloadUsesProjectNamespaceAndOwnershipLabels(t *testing.T) {
	workload := mustRender(t, fullApp(), newResolver())

	for name, got := range map[string]string{
		"deployment":    workload.Deployment.Namespace,
		"egress policy": workload.EgressPolicy.GetNamespace(),
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
	if got := workload.EgressPolicy.GetLabels()["excalibase.io/app"]; got != "app-01H" {
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
		if _, err := RenderAppWorkload(testNamespace, app, newResolver(), testRenderOptions); err == nil {
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

// A secret and a credential-bearing reference become secretKeyRefs into the
// app's own Secret; the values live there and nowhere in the Deployment.
func TestRenderAppWorkloadSecretValuesGoToTheAppSecret(t *testing.T) {
	workload := mustRender(t, fullApp(), newResolver())
	secretName := AppEnvSecretName("web")
	env := map[string]corev1.EnvVar{}
	for _, e := range workload.Deployment.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e
	}
	for _, name := range []string{"STRIPE_KEY", "DATABASE_URL"} {
		e := env[name]
		if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("%s = %+v, want a secretKeyRef", name, e)
		}
		if ref := e.ValueFrom.SecretKeyRef; ref.Name != secretName || ref.Key != name {
			t.Fatalf("%s secretKeyRef = %+v, want %s/%s", name, ref, secretName, name)
		}
	}
	if env["PGHOST"].ValueFrom != nil {
		t.Errorf("PGHOST carries no credential and stays a plain value, got %+v", env["PGHOST"])
	}
}

func TestRenderAppWorkloadEnvSecretHoldsTheValues(t *testing.T) {
	secret := mustRender(t, fullApp(), newResolver()).EnvSecret
	if secret == nil {
		t.Fatal("an app with secret values must render its env Secret")
	}
	if secret.Name != AppEnvSecretName("web") || secret.Namespace != testNamespace || secret.Type != corev1.SecretTypeOpaque {
		t.Fatalf("secret meta = %s/%s type %s", secret.Namespace, secret.Name, secret.Type)
	}
	if secret.Labels["excalibase.io/app"] != "app-01H" {
		t.Errorf("the Secret must carry the app's ownership labels, got %v", secret.Labels)
	}
	want := map[string]string{"STRIPE_KEY": testStripeKey, "DATABASE_URL": testDatabaseURL}
	got := map[string]string{}
	for key, value := range secret.Data {
		got[key] = string(value)
	}
	if !maps.Equal(got, want) {
		t.Errorf("secret data = %v, want %v", got, want)
	}
}

// No value an app is given in confidence may appear anywhere in the
// Deployment, which anything able to list the namespace can read.
func TestRenderAppWorkloadDeploymentCarriesNoSecretValue(t *testing.T) {
	app := fullApp()
	app.Env = append(app.Env, apphost.EnvVar{Name: "PGPASSWORD", Kind: apphost.KindReference,
		Reference: &apphost.ReferenceTarget{SourceKind: apphost.SourceDatabase, SourceName: "proj-abc", Variable: "PGPASSWORD"}})
	workload := mustRender(t, app, newResolver())
	encoded, err := yaml.Marshal(workload.Deployment)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, value := range []string{testStripeKey, testDatabaseURL, "owner-pass-9f2"} {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("the Deployment carries a secret value %q:\n%s", value, encoded)
		}
	}
}

// Pods roll on every deploy of an app with secret values, since a value can
// change without the record changing; the values themselves are never hashed
// into an annotation a namespace reader could brute-force.
func TestRenderAppWorkloadSecretValuesRollThePodsPerDeploy(t *testing.T) {
	workload := mustRender(t, fullApp(), newResolver())
	if got := workload.Deployment.Spec.Template.Annotations[appEnvRevisionAnnotation]; got != "7" {
		t.Fatalf("env revision annotation = %q, want 7", got)
	}
	if _, err := RenderAppWorkload(testNamespace, fullApp(), newResolver(), AppRenderOptions{RuntimeClass: testRuntimeClass, Route: testRoute}); !errors.Is(err, ErrRenderApp) {
		t.Fatalf("secret values with no env revision must be refused, got %v", err)
	}
	plain := mustRender(t, minimalApp(), newResolver())
	if plain.EnvSecret != nil {
		t.Error("an app with no secret values renders no Secret")
	}
	if _, ok := plain.Deployment.Spec.Template.Annotations[appEnvRevisionAnnotation]; ok {
		t.Error("an app with no secret values must not roll on every deploy")
	}
}

// A value that resolves empty is never deployed: the container would start
// with a blank credential and fail somewhere the customer cannot see.
func TestRenderAppWorkloadRefusesAnEmptyResolvedValue(t *testing.T) {
	app := fullApp()
	for _, name := range []string{"STRIPE_KEY", "DATABASE_URL", "PGHOST"} {
		resolver := newResolver()
		resolver.valueFor = map[string]string{name: "", ownSecret(app, "STRIPE_KEY").Path: "x"}
		if name == "STRIPE_KEY" {
			resolver.valueFor = map[string]string{ownSecret(app, "STRIPE_KEY").Path: ""}
		}
		_, err := RenderAppWorkload(testNamespace, app, resolver, testRenderOptions)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s resolving empty must be refused naming it, got %v", name, err)
		}
	}
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
	if _, err := RenderAppWorkload(testNamespace, fullApp(), resolver, testRenderOptions); err == nil {
		t.Fatal("an unresolvable reference must be refused")
	}
	if _, err := RenderAppWorkload(testNamespace, fullApp(), nil, testRenderOptions); err == nil {
		t.Fatal("an app with references and no resolver must be refused")
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

	// Without a health path the app must still prove it listens, or a crashing app reads as deployed.
	tcp := mustRender(t, minimalApp(), newResolver()).Deployment
	tcpProbe := tcp.Spec.Template.Spec.Containers[0].ReadinessProbe
	if tcpProbe == nil || tcpProbe.TCPSocket == nil || tcpProbe.TCPSocket.Port.IntValue() != 8080 {
		t.Fatalf("an app with no health check must get a TCP probe on its port, got %+v", tcpProbe)
	}
	if tcp.Spec.MinReadySeconds != appMinReadySeconds {
		t.Errorf("minReadySeconds = %d, want %d", tcp.Spec.MinReadySeconds, appMinReadySeconds)
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

// Root is allowed only because the sandbox, not the uid, is the boundary.
func TestRenderAppWorkloadPodSecurityContext(t *testing.T) {
	spec := mustRender(t, fullApp(), newResolver()).Deployment.Spec.Template.Spec
	security := spec.SecurityContext
	if security == nil {
		t.Fatal("pod security context must be set")
	}
	if security.RunAsNonRoot != nil {
		t.Error("root images run under the sandbox; RunAsNonRoot must be unset")
	}
	if security.SeccompProfile == nil || security.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod seccomp profile = %+v, want RuntimeDefault", security.SeccompProfile)
	}
	if security.RunAsUser != nil {
		t.Error("must not force a uid")
	}
}

func TestRenderAppWorkloadRunsUnderTheSandboxRuntime(t *testing.T) {
	spec := mustRender(t, fullApp(), newResolver()).Deployment.Spec.Template.Spec
	if spec.RuntimeClassName == nil || *spec.RuntimeClassName != testRuntimeClass {
		t.Fatalf("runtimeClassName = %v, want %q", spec.RuntimeClassName, testRenderOptions)
	}
}

func TestRenderAppWorkloadRefusesNoRuntimeClass(t *testing.T) {
	if _, err := RenderAppWorkload(testNamespace, minimalApp(), newResolver(), AppRenderOptions{Route: testRoute}); !errors.Is(err, ErrRenderApp) {
		t.Fatalf("an app with no sandbox runtime must be refused, got %v", err)
	}
}

// Isolation must not depend on host namespaces even accidentally: these are
// zero-valued by default, and asserted so a future change cannot flip them silently.
func TestRenderAppWorkloadNoHostNamespaces(t *testing.T) {
	spec := mustRender(t, fullApp(), newResolver()).Deployment.Spec.Template.Spec
	if spec.HostNetwork {
		t.Error("hostNetwork must be false")
	}
	if spec.HostPID {
		t.Error("hostPID must be false")
	}
	if spec.HostIPC {
		t.Error("hostIPC must be false")
	}
}

func TestRenderAppWorkloadContainerSecurityContext(t *testing.T) {
	container := mustRender(t, fullApp(), newResolver()).Deployment.Spec.Template.Spec.Containers[0]
	security := container.SecurityContext
	if security == nil {
		t.Fatal("container security context must be set")
	}
	if security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation {
		t.Error("container must set AllowPrivilegeEscalation false")
	}
	if security.ReadOnlyRootFilesystem == nil || *security.ReadOnlyRootFilesystem {
		t.Error("container root filesystem must be explicitly writable")
	}
	if security.RunAsNonRoot != nil {
		t.Error("container must not require RunAsNonRoot")
	}
	want := corev1.Capabilities{
		Drop: []corev1.Capability{"ALL"},
		Add: []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "SETUID", "SETGID",
			"SETPCAP", "SETFCAP", "KILL", "NET_BIND_SERVICE", "SYS_CHROOT", "MKNOD", "AUDIT_WRITE"},
	}
	if security.Capabilities == nil || !reflect.DeepEqual(*security.Capabilities, want) {
		t.Fatalf("container capabilities = %+v, want %+v", security.Capabilities, want)
	}
	for _, forbidden := range []corev1.Capability{"NET_RAW", "SYS_ADMIN", "NET_ADMIN", "SYS_PTRACE", "SYS_MODULE"} {
		if slices.Contains(security.Capabilities.Add, forbidden) {
			t.Errorf("container must not be granted %s", forbidden)
		}
	}
}

func TestRenderAppWorkloadMountsNoVolumes(t *testing.T) {
	spec := mustRender(t, fullApp(), newResolver()).Deployment.Spec.Template.Spec
	if len(spec.Volumes) != 0 || len(spec.Containers[0].VolumeMounts) != 0 {
		t.Fatalf("volumes = %+v, mounts = %+v, want none", spec.Volumes, spec.Containers[0].VolumeMounts)
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
			if _, err := RenderAppWorkload(testNamespace, app, newResolver(), testRenderOptions); err == nil {
				t.Error("must be refused")
			}
		})
	}
	if _, err := RenderAppWorkload("", minimalApp(), newResolver(), testRenderOptions); err == nil {
		t.Error("an empty namespace must be refused")
	}
	if _, err := RenderAppWorkload(testNamespace, nil, newResolver(), testRenderOptions); err == nil {
		t.Error("a nil app must be refused")
	}
}

// egressSpec reads the rendered policy back through the same types the renderer wrote.
func egressSpec(t *testing.T, workload *AppWorkload) ciliumPolicySpec {
	t.Helper()
	var spec ciliumPolicySpec
	content, _, err := unstructured.NestedMap(workload.EgressPolicy.Object, "spec")
	if err != nil {
		t.Fatalf("policy spec: %v", err)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &spec); err != nil {
		t.Fatalf("decode policy spec: %v", err)
	}
	return spec
}

func TestRenderAppWorkloadEgressPolicyIsCilium(t *testing.T) {
	workload := mustRender(t, fullApp(), newResolver())
	policy := workload.EgressPolicy
	if policy.GetAPIVersion() != "cilium.io/v2" || policy.GetKind() != "CiliumNetworkPolicy" {
		t.Fatalf("policy is %s %s, want cilium.io/v2 CiliumNetworkPolicy", policy.GetAPIVersion(), policy.GetKind())
	}
	if policy.GetName() != AppEgressPolicyName("web") {
		t.Errorf("policy name = %q", policy.GetName())
	}
	selector := egressSpec(t, workload).EndpointSelector.MatchLabels
	if !maps.Equal(selector, workload.Deployment.Spec.Selector.MatchLabels) {
		t.Errorf("endpoint selector %v must select exactly the app's pods %v", selector, workload.Deployment.Spec.Selector.MatchLabels)
	}
}

func TestRenderAppWorkloadEgressPolicy(t *testing.T) {
	spec := egressSpec(t, mustRender(t, fullApp(), newResolver()))

	if len(spec.Egress) != 3 {
		t.Fatalf("expected a DNS rule, a database rule and an internet rule, got %d", len(spec.Egress))
	}
	assertDNSRule(t, spec.Egress[0])
	assertDatabaseRuleIsLocal(t, spec.Egress[1])
	assertInternetRuleIsWorldOnly(t, spec.Egress[2])
	assertDenyRules(t, spec.EgressDeny, wantDeniedRanges)
}

// Spelled out rather than read from the renderer, so dropping a range fails here.
var wantDeniedRanges = []string{"169.254.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
	"127.0.0.0/8", "0.0.0.0/8", "224.0.0.0/4", "240.0.0.0/4"}

func assertDNSRule(t *testing.T, rule ciliumEgressRule) {
	t.Helper()
	if len(rule.ToEndpoints) != 1 || !maps.Equal(rule.ToEndpoints[0].MatchLabels,
		map[string]string{"k8s:io.kubernetes.pod.namespace": "kube-system", "k8s:k8s-app": "kube-dns"}) {
		t.Errorf("the DNS rule must select kube-dns in kube-system only, got %+v", rule.ToEndpoints)
	}
	assertPorts(t, "DNS", rule.ToPorts, ciliumPort{Port: "53", Protocol: "UDP"}, ciliumPort{Port: "53", Protocol: "TCP"})
	assertNoAddressPeers(t, rule)
}

// An empty selector with no namespace label stays in the policy's namespace, so no other tenant.
func assertDatabaseRuleIsLocal(t *testing.T, rule ciliumEgressRule) {
	t.Helper()
	if len(rule.ToEndpoints) != 1 || len(rule.ToEndpoints[0].MatchLabels) != 0 || len(rule.ToEndpoints[0].MatchExpressions) != 0 {
		t.Errorf("the database rule must be confined to this namespace, got %+v", rule.ToEndpoints)
	}
	assertPorts(t, "database", rule.ToPorts, ciliumPort{Port: "5432", Protocol: "TCP"})
	assertNoAddressPeers(t, rule)
}

func assertNoAddressPeers(t *testing.T, rule ciliumEgressRule) {
	t.Helper()
	if len(rule.ToEntities) != 0 || len(rule.ToCIDRSet) != 0 {
		t.Errorf("only the internet rule may open entities or ranges: %+v", rule)
	}
}

// world is the only entity: cluster, host, remote-node and kube-apiserver stay closed by identity.
func assertInternetRuleIsWorldOnly(t *testing.T, rule ciliumEgressRule) {
	t.Helper()
	if !slices.Equal(rule.ToEntities, []string{"world"}) {
		t.Errorf("the internet rule must be the world entity only, got %v", rule.ToEntities)
	}
	if len(rule.ToEndpoints) != 0 || len(rule.ToCIDRSet) != 0 || len(rule.ToPorts) != 0 {
		t.Errorf("the internet rule must be every port to world and nothing else, got %+v", rule)
	}
}

func assertDenyRules(t *testing.T, deny []ciliumEgressRule, wantCIDRs []string) {
	t.Helper()
	if len(deny) != 2 {
		t.Fatalf("expected a range deny and an SMTP deny, got %d", len(deny))
	}
	var got []string
	for _, cidr := range deny[0].ToCIDRSet {
		got = append(got, cidr.CIDR)
	}
	if !slices.Equal(got, wantCIDRs) {
		t.Errorf("denied ranges = %v, want %v", got, wantCIDRs)
	}
	smtp := deny[1]
	if !slices.Equal(smtp.ToEntities, []string{"world"}) {
		t.Errorf("SMTP deny must target world, got %v", smtp.ToEntities)
	}
	assertPorts(t, "SMTP deny", smtp.ToPorts, ciliumPort{Port: "25", Protocol: "TCP"})
}

func assertPorts(t *testing.T, rule string, got []ciliumPortRule, want ...ciliumPort) {
	t.Helper()
	if !reflect.DeepEqual(got, []ciliumPortRule{{Ports: want}}) {
		t.Errorf("%s ports = %+v, want exactly %+v", rule, got, want)
	}
}

// Extras are additional to the fixed ranges, never a replacement.
func TestRenderAppWorkloadEgressPolicyExtraDenyCIDRs(t *testing.T) {
	workload, err := RenderAppWorkload(testNamespace, minimalApp(), newResolver(), AppRenderOptions{
		RuntimeClass: testRuntimeClass, ExtraDenyCIDRs: []string{"203.0.113.9/32", "198.51.100.0/24"}, Route: testRoute,
	})
	if err != nil {
		t.Fatalf("RenderAppWorkload: %v", err)
	}
	want := append(slices.Clone(wantDeniedRanges), "203.0.113.9/32", "198.51.100.0/24")
	assertDenyRules(t, egressSpec(t, workload).EgressDeny, want)
}

func TestRenderAppWorkloadEgressPolicyWithoutDatabase(t *testing.T) {
	spec := egressSpec(t, mustRender(t, minimalApp(), newResolver()))
	if len(spec.Egress) != 2 {
		t.Fatalf("an app with no reference needs DNS and internet only, got %d rules", len(spec.Egress))
	}
	for _, rule := range spec.Egress {
		for _, selector := range rule.ToEndpoints {
			if selector.MatchLabels["k8s:io.kubernetes.pod.namespace"] != "kube-system" {
				t.Errorf("an app with no database reference must not reach its own namespace: %+v", rule)
			}
		}
	}
}

// The same app renders byte-identical manifests every time, so a later
// reconcile can compare what it finds against what it wants.
func TestRenderAppWorkloadIsDeterministic(t *testing.T) {
	first := renderYAML(t, fullApp())
	for i := 0; i < 20; i++ {
		if renderYAML(t, fullApp()) != first {
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
	if mustRender(t, changed, newResolver()).Deployment.Spec.Template.Annotations[appConfigHashAnnotation] == hash {
		t.Error("a changed image must change the config hash")
	}
}

func renderYAML(t *testing.T, app *apphost.App) string {
	t.Helper()
	return workloadYAML(t, mustRender(t, app, newResolver()))
}

func workloadYAML(t *testing.T, workload *AppWorkload) string {
	t.Helper()
	deployment := workload.Deployment.DeepCopy()
	deployment.TypeMeta = metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment"}

	objects := []interface{}{deployment}
	if workload.EnvSecret != nil {
		secret := workload.EnvSecret.DeepCopy()
		secret.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}
		objects = append(objects, secret)
	}
	service := workload.Service.DeepCopy()
	service.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}
	ingress := workload.Ingress.DeepCopy()
	ingress.TypeMeta = metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"}
	objects = append(objects, workload.EgressPolicy.Object, service, ingress, workload.IngressPolicy.Object)

	var out strings.Builder
	for _, obj := range objects {
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
		t.Run(name, func(t *testing.T) { assertGolden(t, name, renderYAML(t, app)) })
	}
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	assertGoldenAt(t, filepath.Join("testdata", "app_workload", name+".yaml"), got)
}

func assertGoldenAt(t *testing.T, path, got string) {
	t.Helper()
	if *updateGolden {
		writeGolden(t, path, got)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update-golden): %v", err)
	}
	if got != string(want) {
		t.Errorf("rendered manifests differ from %s\n--- got ---\n%s", path, got)
	}
}

func writeGolden(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create golden dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
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
		"secret resolved to nothing": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindSecret, Secret: ownSecret(app, "X")},
			resolver: &brokenResolver{},
		},
		"reference resolved to nothing": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindReference, Reference: target},
			resolver: &brokenResolver{},
		},
		"secret with no resolver": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindSecret, Secret: ownSecret(app, "X")},
		},
		"reference with no resolver": {
			declared: apphost.EnvVar{Name: "X", Kind: apphost.KindReference, Reference: target},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newEnvRenderer(app, tc.resolver).render(tc.declared); err == nil {
				t.Error("must be refused")
			}
		})
	}
}

// brokenResolver answers with empty values, standing in for a resolver
// implementation that goes wrong.
type brokenResolver struct{}

func (b *brokenResolver) ResolveReference(string, apphost.ResolvedReference) (string, error) {
	return "", nil
}

func (b *brokenResolver) ResolveSecret(string, apphost.SecretRef) (string, error) {
	return "", nil
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
