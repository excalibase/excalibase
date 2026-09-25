package k8s

// It renders an app (apphost.App) into a Deployment, its Service and Ingress,
// and the CiliumNetworkPolicies fencing its egress and ingress, in the project's namespace.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
)

const (
	// appComponentLabel marks every object one app owns, for teardown by label.
	appComponentLabel = "app"
	// appManagedByValue distinguishes rendered objects from operator/customer ones.
	appManagedByValue = "excalibase-provisioning"
	// appConfigHashAnnotation rolls the Deployment when the record changes.
	appConfigHashAnnotation = "excalibase.io/app-config-hash"
	// appEnvRevisionAnnotation rolls the Deployment on each deploy of an app
	// with secret values, which can change while the record does not.
	appEnvRevisionAnnotation = "excalibase.io/app-env-revision"
	// appObjectPrefix avoids colliding with CNPG's and the platform's own objects.
	appObjectPrefix = "app-"
	// maxLabelValueLength is Kubernetes' limit; checked rather than truncated
	// since a truncated id would collide with a different app's label.
	maxLabelValueLength = 63
	dnsPortNumber       = 53
	postgresPortNumber  = 5432
)

// namespacePattern is the DNS-1123 label a project namespace must be; an
// unparseable namespace is refused rather than risk the API server accepting it as something else.
var namespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ErrRenderApp marks every refusal this renderer makes.
var ErrRenderApp = errors.New("render app workload")

// SecretKeySelector names a key inside a Kubernetes Secret; it has nowhere to put a value.
type SecretKeySelector struct {
	SecretName string
	Key        string
}

// Resolver reads, at deploy time, the values an app record only points at, so
// nothing durable in the platform's own tables holds them.
type Resolver interface {
	// ResolveReference always resolves at internal cluster scope.
	ResolveReference(projectID string, ref apphost.ResolvedReference) (string, error)
	// ResolveSecret reads the value stored at the app's own vault entry.
	ResolveSecret(projectID string, ref apphost.SecretRef) (string, error)
}

// AppWorkload is one app's rendered manifests; a value, applied by nothing here.
type AppWorkload struct {
	Deployment *appsv1.Deployment
	// EnvSecret holds every secret and credential value the container reads,
	// referenced from the Deployment by key; nil when the app has none.
	EnvSecret     *corev1.Secret
	EgressPolicy  *unstructured.Unstructured
	Service       *corev1.Service
	Ingress       *networkingv1.Ingress
	IngressPolicy *unstructured.Unstructured
}

// credentialBearingVariables must never resolve to a plain value: a
// Deployment is readable by anything that can list the namespace.
var credentialBearingVariables = map[string]bool{
	"PGPASSWORD":   true,
	"DATABASE_URL": true,
}

// AppRenderOptions are the operator settings every app workload is rendered with.
type AppRenderOptions struct {
	// RuntimeClass is the sandbox RuntimeClass every app pod runs under.
	RuntimeClass string
	// ExtraDenyCIDRs are APP_EGRESS_EXTRA_DENY_CIDRS, already validated by config.
	ExtraDenyCIDRs []string
	Route          AppRouteOptions
	// EnvRevision is set per deploy by the deploy service; required once the
	// app has secret values, so their changes roll the pods.
	EnvRevision string
}

// AppObjectName is the name the app's Deployment holds in the project namespace.
func AppObjectName(appName string) string { return appObjectPrefix + appName }

// AppEnvSecretName is the name of the Secret holding the app's secret values.
func AppEnvSecretName(appName string) string { return AppObjectName(appName) + appEnvSecretSuffix }

const appEnvSecretSuffix = "-env"

// AppEgressPolicyName is the name of the app's egress fence.
func AppEgressPolicyName(appName string) string { return appObjectPrefix + appName + "-egress" }

// RenderAppWorkload renders the app into the project's namespace. Every input
// is checked and nothing is defaulted; the output is a pure function of its arguments.
func RenderAppWorkload(namespace string, app *apphost.App, resolver Resolver, opts AppRenderOptions) (*AppWorkload, error) {
	if err := validateRenderInputs(namespace, app, opts); err != nil {
		return nil, err
	}
	tier, err := config.GetAppTierConfig(app.Tier)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRenderApp, err)
	}

	envRenderer := newEnvRenderer(app, resolver)
	env, err := envRenderer.renderAll()
	if err != nil {
		return nil, err
	}
	envSecret := envRenderer.secret(namespace)
	if envSecret != nil && opts.EnvRevision == "" {
		return nil, fmt.Errorf("%w: an app with secret values needs an env revision", ErrRenderApp)
	}
	resources, err := appResourceRequirements(tier)
	if err != nil {
		return nil, err
	}

	deployment, err := buildAppDeployment(namespace, app, env, resources, opts.RuntimeClass)
	if err != nil {
		return nil, err
	}
	if envSecret != nil {
		deployment.Spec.Template.Annotations[appEnvRevisionAnnotation] = opts.EnvRevision
	}
	policy, err := buildAppEgressPolicy(namespace, app, opts.ExtraDenyCIDRs)
	if err != nil {
		return nil, err
	}
	route, err := buildAppRoute(namespace, app, opts.Route)
	if err != nil {
		return nil, err
	}
	return &AppWorkload{
		Deployment: deployment, EnvSecret: envSecret, EgressPolicy: policy,
		Service: route.service, Ingress: route.ingress, IngressPolicy: route.policy,
	}, nil
}

func validateRenderInputs(namespace string, app *apphost.App, opts AppRenderOptions) error {
	if app == nil {
		return fmt.Errorf("%w: no app", ErrRenderApp)
	}
	if opts.RuntimeClass == "" {
		return fmt.Errorf("%w: no sandbox runtime class", ErrRenderApp)
	}
	if !namespacePattern.MatchString(namespace) {
		return fmt.Errorf("%w: %q is not a project namespace", ErrRenderApp, namespace)
	}
	if err := app.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrRenderApp, err)
	}
	for what, value := range map[string]string{"app id": app.ID, "project id": app.ProjectID} {
		if len(value) > maxLabelValueLength {
			return fmt.Errorf("%w: %s %q exceeds %d characters and cannot be a label",
				ErrRenderApp, what, value, maxLabelValueLength)
		}
	}
	return nil
}

// appLabels identify and clean up everything one app owns; app id is the stable key.
func appLabels(app *apphost.App) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       app.Name,
		"app.kubernetes.io/component":  appComponentLabel,
		"app.kubernetes.io/managed-by": appManagedByValue,
		"excalibase.io/component":      appComponentLabel,
		"excalibase.io/app":            app.ID,
		"excalibase.io/project":        app.ProjectID,
		"excalibase.io/tier":           strings.ToLower(string(app.Tier)),
	}
}

// appSelectorLabels is the app's identity only: a Deployment's selector is
// immutable, so the tier (unlike Deno's renderer) must stay out of it.
func appSelectorLabels(app *apphost.App) map[string]string {
	return map[string]string{
		"excalibase.io/component": appComponentLabel,
		"excalibase.io/app":       app.ID,
	}
}

func appResourceRequirements(tier config.AppTierConfig) (corev1.ResourceRequirements, error) {
	parsed := map[string]resource.Quantity{}
	for field, value := range map[string]string{
		"cpu request": tier.CPURequest, "cpu limit": tier.CPULimit,
		"memory request": tier.MemoryRequest, "memory limit": tier.MemoryLimit,
	} {
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return corev1.ResourceRequirements{}, fmt.Errorf("%w: tier %s %q: %w", ErrRenderApp, field, value, err)
		}
		parsed[field] = quantity
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    parsed["cpu request"],
			corev1.ResourceMemory: parsed["memory request"],
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    parsed["cpu limit"],
			corev1.ResourceMemory: parsed["memory limit"],
		},
	}, nil
}

// envRenderer walks variables in authored order so the rendered list is
// stable, collecting every value that must not sit in the Deployment into the
// app's env Secret.
type envRenderer struct {
	app        *apphost.App
	resolver   Resolver
	secretName string
	secretData map[string][]byte
}

func newEnvRenderer(app *apphost.App, resolver Resolver) *envRenderer {
	return &envRenderer{
		app: app, resolver: resolver,
		secretName: AppEnvSecretName(app.Name),
		secretData: map[string][]byte{},
	}
}

func (r *envRenderer) renderAll() ([]corev1.EnvVar, error) {
	if len(r.app.Env) == 0 {
		return nil, nil
	}
	out := make([]corev1.EnvVar, 0, len(r.app.Env))
	for _, declared := range r.app.Env {
		rendered, err := r.render(declared)
		if err != nil {
			return nil, err
		}
		out = append(out, rendered)
	}
	return out, nil
}

// render re-checks payload presence even after App.Validate: tenant-supplied
// data must be refused, never panic the control plane.
func (r *envRenderer) render(declared apphost.EnvVar) (corev1.EnvVar, error) {
	switch declared.Kind {
	case apphost.KindLiteral:
		if declared.Value == nil {
			return corev1.EnvVar{}, missingPayload(declared)
		}
		// The empty string is a legitimate value and is rendered as one.
		return corev1.EnvVar{Name: declared.Name, Value: *declared.Value}, nil
	case apphost.KindSecret:
		if declared.Secret == nil {
			return corev1.EnvVar{}, missingPayload(declared)
		}
		return r.renderSecret(declared)
	case apphost.KindReference:
		if declared.Reference == nil {
			return corev1.EnvVar{}, missingPayload(declared)
		}
		return r.renderReference(declared)
	default:
		return corev1.EnvVar{}, fmt.Errorf("%w: variable %q has an unknown kind %q",
			ErrRenderApp, declared.Name, declared.Kind)
	}
}

func missingPayload(declared apphost.EnvVar) error {
	return fmt.Errorf("%w: variable %q is declared %s and carries no %s payload",
		ErrRenderApp, declared.Name, declared.Kind, declared.Kind)
}

func (r *envRenderer) renderSecret(declared apphost.EnvVar) (corev1.EnvVar, error) {
	if r.resolver == nil {
		return corev1.EnvVar{}, fmt.Errorf("%w: %q is a secret and no resolver was given",
			ErrRenderApp, declared.Name)
	}
	value, err := r.resolver.ResolveSecret(r.app.ProjectID, *declared.Secret)
	if err != nil {
		return corev1.EnvVar{}, fmt.Errorf("%w: resolve secret for %q: %w", ErrRenderApp, declared.Name, err)
	}
	if value == "" {
		return corev1.EnvVar{}, fmt.Errorf("%w: resolve secret for %q: no value is stored", ErrRenderApp, declared.Name)
	}
	return r.hide(declared.Name, value)
}

// renderReference resolves to the internal cluster address; unresolvable or
// empty is fatal here, never an empty container variable, and a credential
// only ever reaches the container through the env Secret.
func (r *envRenderer) renderReference(declared apphost.EnvVar) (corev1.EnvVar, error) {
	if r.resolver == nil {
		return corev1.EnvVar{}, fmt.Errorf("%w: %q is a reference and no resolver was given",
			ErrRenderApp, declared.Name)
	}
	reference := apphost.ResolvedReference{
		Name:   declared.Name,
		Target: *declared.Reference,
		Scope:  apphost.ScopeInternal,
	}
	value, err := r.resolver.ResolveReference(r.app.ProjectID, reference)
	if err != nil {
		return corev1.EnvVar{}, fmt.Errorf("%w: resolve %q (%s): %w",
			ErrRenderApp, declared.Name, reference.Target, err)
	}
	if value == "" {
		return corev1.EnvVar{}, fmt.Errorf("%w: resolve %q (%s): resolved to an empty value",
			ErrRenderApp, declared.Name, reference.Target)
	}
	if credentialBearingVariables[declared.Reference.Variable] {
		return r.hide(declared.Name, value)
	}
	return corev1.EnvVar{Name: declared.Name, Value: value}, nil
}

func (r *envRenderer) hide(name, value string) (corev1.EnvVar, error) {
	r.secretData[name] = []byte(value)
	return secretEnvVar(name, SecretKeySelector{SecretName: r.secretName, Key: name})
}

// secret is the app's env Secret, or nil when no variable needed one.
func (r *envRenderer) secret(namespace string) *corev1.Secret {
	if len(r.secretData) == 0 {
		return nil
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.secretName,
			Namespace: namespace,
			Labels:    appLabels(r.app),
		},
		Type: corev1.SecretTypeOpaque,
		Data: r.secretData,
	}
}

func secretEnvVar(name string, selector SecretKeySelector) (corev1.EnvVar, error) {
	if selector.SecretName == "" || selector.Key == "" {
		return corev1.EnvVar{}, fmt.Errorf("%w: %q resolved to an incomplete secret reference", ErrRenderApp, name)
	}
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: selector.SecretName},
				Key:                  selector.Key,
			},
		},
	}, nil
}

func buildAppDeployment(
	namespace string,
	app *apphost.App,
	env []corev1.EnvVar,
	resources corev1.ResourceRequirements,
	runtimeClass string,
) (*appsv1.Deployment, error) {
	container := buildAppContainer(app, env, resources)
	hash, err := appConfigHash(container)
	if err != nil {
		return nil, err
	}
	// Zero included: an absent count means one to Kubernetes, restarting a stopped app.
	replicas := int32(app.Replicas)
	automount := false
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AppObjectName(app.Name),
			Namespace: namespace,
			Labels:    appLabels(app),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas:        &replicas,
			MinReadySeconds: appMinReadySeconds,
			Strategy:        appRolloutStrategy(),
			Selector:        &metav1.LabelSelector{MatchLabels: appSelectorLabels(app)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      appLabels(app),
					Annotations: map[string]string{appConfigHashAnnotation: hash},
				},
				Spec: corev1.PodSpec{
					RuntimeClassName: &runtimeClass,
					// Customer code that escapes must not find a cluster API token beside it.
					AutomountServiceAccountToken: &automount,
					SecurityContext:              podSecurityContext(),
					Containers:                   []corev1.Container{container},
				},
			},
		},
	}, nil
}

// appRolloutStrategy starts a new pod before stopping an old one, so a redeploy never drops below the asked-for count.
func appRolloutStrategy() appsv1.DeploymentStrategy {
	maxUnavailable := intstr.FromInt(0)
	maxSurge := intstr.FromInt(1)
	return appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxUnavailable: &maxUnavailable,
			MaxSurge:       &maxSurge,
		},
	}
}

// podSecurityContext leaves the uid to the image: the sandbox runtime, not the uid, is the boundary.
func podSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// appCapabilities is the standard container set minus NET_RAW, so ordinary root images run unchanged.
var appCapabilities = []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "SETUID", "SETGID",
	"SETPCAP", "SETFCAP", "KILL", "NET_BIND_SERVICE", "SYS_CHROOT", "MKNOD", "AUDIT_WRITE"}

// containerSecurityContext keeps a writable, ephemeral root filesystem.
func containerSecurityContext() *corev1.SecurityContext {
	noEscalation := false
	readOnlyRoot := false
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noEscalation,
		ReadOnlyRootFilesystem:   &readOnlyRoot,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
			Add:  slices.Clone(appCapabilities),
		},
	}
}

func buildAppContainer(app *apphost.App, env []corev1.EnvVar, resources corev1.ResourceRequirements) corev1.Container {
	return corev1.Container{
		Name: app.Name,
		// Used exactly as recorded; EXC-386 fills App.ResolvedDigest once one exists.
		Image:           app.Image,
		ImagePullPolicy: imagePullPolicyFor(app.Image),
		Ports: []corev1.ContainerPort{{
			Name:          "http",
			ContainerPort: int32(app.Port),
			Protocol:      corev1.ProtocolTCP,
		}},
		Env:             env,
		Resources:       resources,
		ReadinessProbe:  appReadinessProbe(app),
		Lifecycle:       appLifecycle(),
		SecurityContext: containerSecurityContext(),
	}
}

// appDrainSeconds keeps a stopping pod serving until the ingress controller has
// dropped it from its endpoints; a kubelet sleep, so the image needs no sleep binary.
const appDrainSeconds = 10

func appLifecycle() *corev1.Lifecycle {
	return &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{Sleep: &corev1.SleepAction{Seconds: appDrainSeconds}}}
}

// imagePullPolicyFor re-pulls a mutable tag: a node caching a different build
// under the same tag would make what runs depend on which node the pod landed on.
func imagePullPolicyFor(image string) corev1.PullPolicy {
	if strings.Contains(image, "@") {
		return corev1.PullIfNotPresent
	}
	return corev1.PullAlways
}

// appMinReadySeconds: a pod must stay ready this long to count, so an app that
// crashes right after opening its port does not read as deployed.
const appMinReadySeconds = 10

// appReadinessProbe checks the declared port when no health path is given, never an invented path.
func appReadinessProbe(app *apphost.App) *corev1.Probe {
	handler := corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(app.Port)}}
	if app.HealthCheckPath != "" {
		handler = corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: app.HealthCheckPath, Port: intstr.FromInt(app.Port)}}
	}
	return &corev1.Probe{
		ProbeHandler:        handler,
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
		TimeoutSeconds:      3,
		FailureThreshold:    3,
	}
}

// appConfigHash rolls the Deployment on a changed record; JSON encoding of the
// container is deterministic, so the hash is a function of the rendered output alone.
func appConfigHash(container corev1.Container) (string, error) {
	encoded, err := json.Marshal(container)
	if err != nil {
		return "", fmt.Errorf("%w: hash container: %w", ErrRenderApp, err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:8]), nil
}

// referencesDatabase decides whether the egress fence opens 5432 at all.
func referencesDatabase(app *apphost.App) bool {
	for _, reference := range app.Resolutions() {
		if reference.Target.SourceKind == apphost.SourceDatabase {
			return true
		}
	}
	return false
}
