package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
)

const (
	denoRuntimeName      = "deno-runtime"
	denoEgressPolicyName = "deno-runtime-egress"
	// allowedHostsEnvName is the runtime's outbound allowlist (Deno net permission
	// list, comma-separated). Empty = the worker gets no network at all.
	allowedHostsEnvName = "ALLOWED_HOSTS"
	// egressHashAnnotation sits on the pod template so a changed allowlist
	// changes the template and the Deployment rolls (EXC-348).
	egressHashAnnotation = "excalibase.io/egress-hash"
)

// denoTierResources maps a project tier to Deno runtime pod resource requests
// and limits. Free tier is intentionally tight (minimal footprint for hobby
// projects), enterprise has more headroom. Limits are burstable, requests
// are what K8s actually reserves on the node.
func denoTierResources(tier string) (cpuReq, cpuLim, memReq, memLim string) {
	switch strings.ToUpper(tier) {
	case "ENTERPRISE":
		return "100m", "1000m", "256Mi", "512Mi"
	case "STANDARD":
		return "50m", "500m", "128Mi", "256Mi"
	default: // FREE or unknown
		return "10m", "200m", "64Mi", "128Mi"
	}
}

// EnsureDenoRuntime creates the deno-runtime Deployment + Service + egress
// NetworkPolicy in the namespace, or — when they exist — re-renders the
// project's outbound allowlist into them. Idempotent and cheap to call on
// every function deploy: an unchanged allowlist touches nothing. A changed
// one updates the pod env (which rolls the Deployment) and the NetworkPolicy
// in lock-step, so the sandbox permission and the infra backstop never
// disagree. The runtime is reachable at
// http://deno-runtime.{namespace}.svc.cluster.local:8000
func (c *Client) EnsureDenoRuntime(ctx context.Context, namespace string, spec DenoRuntimeSpec) error {
	if spec.Image == "" {
		spec.Image = "excalibase/deno-runtime:latest"
	}
	existing, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, denoRuntimeName, metav1.GetOptions{})
	switch {
	case err == nil:
		return c.reconcileDenoEgress(ctx, namespace, existing, spec)
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("check existing deployment: %w", err)
	}

	dep := buildDenoDeployment(namespace, spec)
	if _, err := c.clientset.AppsV1().Deployments(namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create deno deployment: %w", err)
	}
	svc := buildDenoService(namespace, dep.Labels)
	if _, err := c.clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create deno service: %w", err)
	}
	return c.applyDenoEgressPolicy(ctx, namespace, spec.AllowedHosts)
}

// reconcileDenoEgress updates a live runtime when its allowlist differs from
// the desired one. The env value is the single comparison key: it is exactly
// what the runtime reads, so "equal env" means "already rendered".
func (c *Client) reconcileDenoEgress(ctx context.Context, namespace string, dep *appsv1.Deployment, spec DenoRuntimeSpec) error {
	want := edgefn.EgressEnvValue(spec.AllowedHosts)
	if currentAllowedHosts(dep) == want {
		return nil
	}
	updated := dep.DeepCopy()
	setAllowedHosts(updated, want)
	if _, err := c.clientset.AppsV1().Deployments(namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update deno deployment egress: %w", err)
	}
	return c.applyDenoEgressPolicy(ctx, namespace, spec.AllowedHosts)
}

func currentAllowedHosts(dep *appsv1.Deployment) string {
	for _, container := range dep.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == allowedHostsEnvName {
				return env.Value
			}
		}
	}
	return ""
}

// setAllowedHosts writes the env on every container (there is one) and stamps
// the pod template so the change is a rollout even if a future env-less
// runtime image ignores ALLOWED_HOSTS.
func setAllowedHosts(dep *appsv1.Deployment, value string) {
	for ci := range dep.Spec.Template.Spec.Containers {
		envs := dep.Spec.Template.Spec.Containers[ci].Env
		found := false
		for ei := range envs {
			if envs[ei].Name == allowedHostsEnvName {
				envs[ei].Value = value
				found = true
			}
		}
		if !found {
			envs = append(envs, corev1.EnvVar{Name: allowedHostsEnvName, Value: value})
		}
		dep.Spec.Template.Spec.Containers[ci].Env = envs
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations[egressHashAnnotation] = egressHash(value)
}

func egressHash(allowedHosts string) string {
	sum := sha256.Sum256([]byte(allowedHosts))
	return hex.EncodeToString(sum[:8])
}

func buildDenoDeployment(namespace string, spec DenoRuntimeSpec) *appsv1.Deployment {
	labels := map[string]string{
		"app":                     denoRuntimeName,
		"excalibase.io/component": "edgefn",
		"excalibase.io/tier":      strings.ToLower(spec.Tier),
	}
	one := int32(1)
	falseVal := false
	allowed := edgefn.EgressEnvValue(spec.AllowedHosts)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: denoRuntimeName, Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: map[string]string{egressHashAnnotation: egressHash(allowed)},
				},
				Spec: corev1.PodSpec{
					// The Deno runtime executes tenant-authored code and never calls
					// the k8s API — don't mount a service-account token it could pivot
					// with if a function escapes its isolate (EXC-322).
					AutomountServiceAccountToken: &falseVal,
					Containers:                   []corev1.Container{buildDenoContainer(spec, allowed)},
				},
			},
		},
	}
}

func buildDenoContainer(spec DenoRuntimeSpec, allowedHosts string) corev1.Container {
	return corev1.Container{
		Name:            denoRuntimeName,
		Image:           spec.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports:           []corev1.ContainerPort{{ContainerPort: 8000, Protocol: corev1.ProtocolTCP}},
		Env: []corev1.EnvVar{
			{Name: "RUNTIME_SECRET", Value: spec.RuntimeSecret},
			// The project's outbound allowlist (EXC-348). Empty = no network
			// for user code, which is the default for a new project.
			{Name: allowedHostsEnvName, Value: allowedHosts},
		},
		Resources: denoResourceRequirements(spec.Tier),
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt(8000)},
			},
			InitialDelaySeconds: 1,
			PeriodSeconds:       3,
		},
	}
}

func denoResourceRequirements(tier string) corev1.ResourceRequirements {
	cpuReqStr, cpuLimStr, memReqStr, memLimStr := denoTierResources(tier)
	cpuReq, _ := resource.ParseQuantity(cpuReqStr)
	cpuLim, _ := resource.ParseQuantity(cpuLimStr)
	memReq, _ := resource.ParseQuantity(memReqStr)
	memLim, _ := resource.ParseQuantity(memLimStr)
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: cpuReq, corev1.ResourceMemory: memReq},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: cpuLim, corev1.ResourceMemory: memLim},
	}
}

func buildDenoService(namespace string, labels map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: denoRuntimeName, Namespace: namespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Port:       8000,
				TargetPort: intstr.FromInt(8000),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// platformNamespace is the namespace provisioning itself runs in — the peer the
// Deno runtime's metadata callback targets. Read from the pod's own
// serviceaccount namespace file (provisioning is the one component that keeps
// its k8s token), overridable via POD_NAMESPACE, defaulting to the chart's value.
func platformNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(b)); ns != "" {
			return ns
		}
	}
	return "excalibase-platform"
}

// applyDenoEgressPolicy creates or re-renders the runtime's egress fence.
func (c *Client) applyDenoEgressPolicy(ctx context.Context, namespace string, allowedHosts []string) error {
	policy := buildDenoEgressPolicy(namespace, allowedHosts)
	policies := c.clientset.NetworkingV1().NetworkPolicies(namespace)
	_, err := policies.Create(ctx, policy, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create deno egress policy: %w", err)
	}
	if _, err := policies.Update(ctx, policy, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update deno egress policy: %w", err)
	}
	return nil
}

// buildDenoEgressPolicy fences the Deno runtime's egress (EXC-330).
//
// The runtime executes tenant-authored code. Deno permissions already deny the
// worker net access beyond its allowlist (DB I/O is brokered by the main
// thread), but the *pod* still opens the Postgres pool — so an escape from the
// isolate would otherwise have the pod's full network reach. This policy is
// the infra backstop under the Deno sandbox: egress is allowed ONLY to DNS,
// to Postgres inside this project's own namespace, to provisioning's API, and
// — when the project has an allowlist — to public internet addresses on the
// allowlisted ports. Everything else is denied by omission — cloud metadata
// (169.254.169.254), Vault / platform-db in the platform namespace, the k8s
// API, and every other tenant's namespace. A NetworkPolicy cannot match
// hostnames, so the per-host part of the allowlist is enforced by the Deno
// permission alone; the policy fences the address space and ports around it.
//
// Egress-only: ingress is untouched so provisioning can still reach /deploy and
// /invoke. Requires a NetworkPolicy-enforcing CNI (Calico/Cilium); with a CNI
// that ignores policies the object is created but not enforced.
func buildDenoEgressPolicy(namespace string, allowedHosts []string) *networkingv1.NetworkPolicy {
	rules := []networkingv1.NetworkPolicyEgressRule{
		denoDNSRule(), denoOwnPostgresRule(), denoProvisioningRule(),
	}
	if len(allowedHosts) > 0 {
		rules = append(rules, denoInternetRule(edgefn.EgressHostPorts(allowedHosts)))
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      denoEgressPolicyName,
			Namespace: namespace,
			Labels:    map[string]string{"excalibase.io/component": "edgefn"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": denoRuntimeName}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}
}

func tcpPort(n int) networkingv1.NetworkPolicyPort {
	tcp := corev1.ProtocolTCP
	port := intstr.FromInt(n)
	return networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &port}
}

// denoDNSRule allows resolution via kube-dns / CoreDNS.
func denoDNSRule() networkingv1.NetworkPolicyEgressRule {
	udp := corev1.ProtocolUDP
	dnsPort := intstr.FromInt(53)
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
			},
		}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dnsPort}, tcpPort(53)},
	}
}

// denoOwnPostgresRule allows this project's own Postgres only. An empty
// PodSelector with no NamespaceSelector means "pods in this namespace" — so
// it cannot reach another tenant's database.
func denoOwnPostgresRule() networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		To:    []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}},
		Ports: []networkingv1.NetworkPolicyPort{tcpPort(5432)},
	}
}

// denoProvisioningRule lets the runtime post captured v2 export metadata back
// to provisioning (EXCALIBASE_PROVISIONING_URL → /internal/runtime/...). That
// callback is fire-and-forget, so without this rule an enforcing CNI would
// drop it *silently*. Scoped to the platform namespace's provisioning pod on
// its API port only.
func denoProvisioningRule() networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kubernetes.io/metadata.name": platformNamespace()},
			},
			PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "provisioning"}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{tcpPort(24005)},
	}
}

// privateRanges are never reachable through the allowlist rule, whatever the
// project lists: RFC-1918, loopback, link-local (cloud metadata), CGNAT and
// this-network — the same classes edgefn.ParseEgressHosts refuses by literal,
// enforced here for names that resolve into them.
var privateRanges = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16",
}

// denoInternetRule opens public IPv4 space on the allowlisted TCP ports.
func denoInternetRule(ports []int) networkingv1.NetworkPolicyEgressRule {
	policyPorts := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
	for _, n := range ports {
		policyPorts = append(policyPorts, tcpPort(n))
	}
	return networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: privateRanges},
		}},
		Ports: policyPorts,
	}
}
