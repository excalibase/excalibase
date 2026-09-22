package k8s

// It renders an app (apphost.App) into a Deployment plus an egress
// NetworkPolicy in the project's namespace. Pod hardening is EXC-380;
// applying the result to a cluster is EXC-386.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// appObjectPrefix avoids colliding with CNPG's and the platform's own objects.
	appObjectPrefix = "app-"
	// maxLabelValueLength is Kubernetes' limit; checked rather than truncated
	// since a truncated id would collide with a different app's label.
	maxLabelValueLength = 63
	dnsPortNumber       = 53
	postgresPortNumber  = 5432
	// tmpVolumeName backs the one writable path a read-only root filesystem gets.
	tmpVolumeName = "tmp"
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

// EnvSource is a resolved variable: a plain value or a Secret reference, never both.
type EnvSource struct {
	Literal *string
	Secret  *SecretKeySelector
}

// Resolver turns the pointers an app record stores into addresses at render
// time, so nothing durable holds a credential. EXC-386 provides the real implementation.
type Resolver interface {
	// ResolveReference always resolves at internal cluster scope.
	ResolveReference(projectID string, ref apphost.ResolvedReference) (EnvSource, error)
	// ResolveSecret returns a selector, never a value.
	ResolveSecret(projectID string, ref apphost.SecretRef) (SecretKeySelector, error)
}

// AppWorkload is one app's rendered manifests; a value, applied by nothing here.
type AppWorkload struct {
	Deployment    *appsv1.Deployment
	NetworkPolicy *networkingv1.NetworkPolicy
}

// credentialBearingVariables must never resolve to a plain value: a
// Deployment is readable by anything that can list the namespace.
var credentialBearingVariables = map[string]bool{
	"PGPASSWORD":   true,
	"DATABASE_URL": true,
}

// AppObjectName is the name the app's Deployment holds in the project namespace.
func AppObjectName(appName string) string { return appObjectPrefix + appName }

// AppEgressPolicyName is the name of the app's egress fence.
func AppEgressPolicyName(appName string) string { return appObjectPrefix + appName + "-egress" }

// RenderAppWorkload renders the app into the project's namespace. Every input
// is checked and nothing is defaulted; the output is a pure function of the app and the resolver.
func RenderAppWorkload(namespace string, app *apphost.App, resolver Resolver) (*AppWorkload, error) {
	if app == nil {
		return nil, fmt.Errorf("%w: no app", ErrRenderApp)
	}
	if !namespacePattern.MatchString(namespace) {
		return nil, fmt.Errorf("%w: %q is not a project namespace", ErrRenderApp, namespace)
	}
	if err := app.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRenderApp, err)
	}
	for what, value := range map[string]string{"app id": app.ID, "project id": app.ProjectID} {
		if len(value) > maxLabelValueLength {
			return nil, fmt.Errorf("%w: %s %q exceeds %d characters and cannot be a label",
				ErrRenderApp, what, value, maxLabelValueLength)
		}
	}
	tier, err := config.GetAppTierConfig(app.Tier)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRenderApp, err)
	}

	env, err := renderAppEnv(app, resolver)
	if err != nil {
		return nil, err
	}
	resources, err := appResourceRequirements(tier)
	if err != nil {
		return nil, err
	}

	deployment, err := buildAppDeployment(namespace, app, env, resources)
	if err != nil {
		return nil, err
	}
	return &AppWorkload{
		Deployment:    deployment,
		NetworkPolicy: buildAppEgressPolicy(namespace, app),
	}, nil
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

// renderAppEnv walks variables in authored order so the rendered list is stable. Nothing iterates a map.
func renderAppEnv(app *apphost.App, resolver Resolver) ([]corev1.EnvVar, error) {
	if len(app.Env) == 0 {
		return nil, nil
	}
	out := make([]corev1.EnvVar, 0, len(app.Env))
	for _, declared := range app.Env {
		rendered, err := renderEnvVar(app, declared, resolver)
		if err != nil {
			return nil, err
		}
		out = append(out, rendered)
	}
	return out, nil
}

// renderEnvVar re-checks payload presence even after App.Validate: tenant-supplied
// data must be refused, never panic the control plane.
func renderEnvVar(app *apphost.App, declared apphost.EnvVar, resolver Resolver) (corev1.EnvVar, error) {
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
		return renderSecretEnv(app, declared, resolver)
	case apphost.KindReference:
		if declared.Reference == nil {
			return corev1.EnvVar{}, missingPayload(declared)
		}
		return renderReferenceEnv(app, declared, resolver)
	default:
		return corev1.EnvVar{}, fmt.Errorf("%w: variable %q has an unknown kind %q",
			ErrRenderApp, declared.Name, declared.Kind)
	}
}

func missingPayload(declared apphost.EnvVar) error {
	return fmt.Errorf("%w: variable %q is declared %s and carries no %s payload",
		ErrRenderApp, declared.Name, declared.Kind, declared.Kind)
}

func renderSecretEnv(app *apphost.App, declared apphost.EnvVar, resolver Resolver) (corev1.EnvVar, error) {
	if resolver == nil {
		return corev1.EnvVar{}, fmt.Errorf("%w: %q is a secret and no resolver was given",
			ErrRenderApp, declared.Name)
	}
	selector, err := resolver.ResolveSecret(app.ProjectID, *declared.Secret)
	if err != nil {
		return corev1.EnvVar{}, fmt.Errorf("%w: resolve secret for %q: %w", ErrRenderApp, declared.Name, err)
	}
	return secretEnvVar(declared.Name, selector)
}

// renderReferenceEnv resolves to the internal cluster address; unresolvable is
// fatal here, not an empty container variable, and a credential may only come back as a Secret.
func renderReferenceEnv(app *apphost.App, declared apphost.EnvVar, resolver Resolver) (corev1.EnvVar, error) {
	if resolver == nil {
		return corev1.EnvVar{}, fmt.Errorf("%w: %q is a reference and no resolver was given",
			ErrRenderApp, declared.Name)
	}
	reference := apphost.ResolvedReference{
		Name:   declared.Name,
		Target: *declared.Reference,
		Scope:  apphost.ScopeInternal,
	}
	source, err := resolver.ResolveReference(app.ProjectID, reference)
	if err != nil {
		return corev1.EnvVar{}, fmt.Errorf("%w: resolve %q (%s): %w",
			ErrRenderApp, declared.Name, reference.Target, err)
	}
	switch {
	case source.Secret != nil && source.Literal != nil:
		return corev1.EnvVar{}, fmt.Errorf("%w: %q resolved to both a value and a secret",
			ErrRenderApp, declared.Name)
	case source.Secret != nil:
		return secretEnvVar(declared.Name, *source.Secret)
	case source.Literal != nil:
		if credentialBearingVariables[declared.Reference.Variable] {
			return corev1.EnvVar{}, fmt.Errorf("%w: %q resolves to %s, which carries a credential and must be a secret reference",
				ErrRenderApp, declared.Name, declared.Reference.Variable)
		}
		return corev1.EnvVar{Name: declared.Name, Value: *source.Literal}, nil
	default:
		return corev1.EnvVar{}, fmt.Errorf("%w: %q resolved to nothing", ErrRenderApp, declared.Name)
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
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: appSelectorLabels(app)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      appLabels(app),
					Annotations: map[string]string{appConfigHashAnnotation: hash},
				},
				Spec: corev1.PodSpec{
					// Unsandboxed customer code that escapes must not find a
					// cluster API token mounted beside it.
					AutomountServiceAccountToken: &automount,
					SecurityContext:              podSecurityContext(),
					Containers:                   []corev1.Container{container},
					Volumes:                      []corev1.Volume{tmpVolume()},
				},
			},
		},
	}, nil
}

// podSecurityContext keeps the customer's image off host root: no forced uid,
// since many images declare their own non-root user, but root is refused outright.
func podSecurityContext() *corev1.PodSecurityContext {
	nonRoot := true
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   &nonRoot,
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// containerSecurityContext closes off escalation and persistence: no new
// privileges, no capabilities, and a filesystem the app cannot write to.
func containerSecurityContext() *corev1.SecurityContext {
	nonRoot := true
	noEscalation := false
	readOnlyRoot := true
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noEscalation,
		ReadOnlyRootFilesystem:   &readOnlyRoot,
		RunAsNonRoot:             &nonRoot,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// tmpVolume is the one writable exception a read-only root filesystem gets,
// bounded so a runaway process cannot exhaust node disk.
func tmpVolume() corev1.Volume {
	sizeLimit := resource.MustParse("64Mi")
	return corev1.Volume{
		Name: tmpVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &sizeLimit},
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
		SecurityContext: containerSecurityContext(),
		VolumeMounts:    []corev1.VolumeMount{{Name: tmpVolumeName, MountPath: "/tmp"}},
	}
}

// imagePullPolicyFor re-pulls a mutable tag: a node caching a different build
// under the same tag would make what runs depend on which node the pod landed on.
func imagePullPolicyFor(image string) corev1.PullPolicy {
	if strings.Contains(image, "@") {
		return corev1.PullIfNotPresent
	}
	return corev1.PullAlways
}

// appReadinessProbe: no declared health check means no probe, not an invented
// path that would mark a healthy app unready.
func appReadinessProbe(app *apphost.App) *corev1.Probe {
	if app.HealthCheckPath == "" {
		return nil
	}
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: app.HealthCheckPath,
				Port: intstr.FromInt(app.Port),
			},
		},
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

// buildAppEgressPolicy fences the app's outbound traffic.
//
// Deno's egress minus the control-plane and internet rules: a customer image
// has no callback to make and no allowlist yet (EXC-383). Needs a policy-enforcing CNI.
func buildAppEgressPolicy(namespace string, app *apphost.App) *networkingv1.NetworkPolicy {
	rules := []networkingv1.NetworkPolicyEgressRule{appDNSRule()}
	if referencesDatabase(app) {
		rules = append(rules, appOwnDatabaseRule())
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AppEgressPolicyName(app.Name),
			Namespace: namespace,
			Labels:    appLabels(app),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: appSelectorLabels(app)},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}
}

// appDNSRule allows kube-dns/CoreDNS only, on port 53.
func appDNSRule() networkingv1.NetworkPolicyEgressRule {
	udp := corev1.ProtocolUDP
	port := intstr.FromInt(dnsPortNumber)
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
			},
		}},
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: &udp, Port: &port},
			tcpPort(dnsPortNumber),
		},
	}
}

// appOwnDatabaseRule allows Postgres inside this project's namespace only.
func appOwnDatabaseRule() networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		To:    []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}},
		Ports: []networkingv1.NetworkPolicyPort{tcpPort(postgresPortNumber)},
	}
}
