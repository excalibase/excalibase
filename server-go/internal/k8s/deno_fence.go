package k8s

import (
	_ "embed" // go:embed carries the fence script

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	// denoFenceContainerName waits for the runtime's egress fence before the runtime starts (EXC-622).
	denoFenceContainerName = "wait-for-fence"
	// denoFenceDNSEnv and denoFenceDeadlineEnv override the nameserver and the
	// 30 s deadline; the rendered container sets neither.
	denoFenceDNSEnv      = "FENCE_DNS"
	denoFenceDeadlineEnv = "FENCE_DEADLINE_MS"
	denoFenceCPU         = "100m"
	denoFenceMemory      = "64Mi"
)

// denoFenceScript is run by the runtime image's own deno, so nothing else is pulled.
//
//go:embed deno_fence.ts
var denoFenceScript string

// denoFenceContainer holds the runtime back until the fence refuses the
// Kubernetes API and DNS answers, and fails the pod after 30 s if it never does.
func denoFenceContainer(image string) corev1.Container {
	noEscalation, readOnlyRoot := false, true
	bound := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(denoFenceCPU),
		corev1.ResourceMemory: resource.MustParse(denoFenceMemory),
	}
	return corev1.Container{
		Name:            denoFenceContainerName,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"deno", "eval", "--ext=ts", denoFenceScript},
		Resources:       corev1.ResourceRequirements{Requests: bound, Limits: bound},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &noEscalation,
			ReadOnlyRootFilesystem:   &readOnlyRoot,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

// hasDenoFence reports whether the runtime already waits for its fence.
func hasDenoFence(spec corev1.PodSpec) bool {
	for _, container := range spec.InitContainers {
		if container.Name == denoFenceContainerName {
			return true
		}
	}
	return false
}
