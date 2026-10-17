package k8s

import (
	_ "embed" // go:embed carries the fence script

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
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
func denoFenceContainer(image, tier string) corev1.Container {
	noEscalation, readOnlyRoot := false, true
	return corev1.Container{
		Name:            denoFenceContainerName,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"deno", "eval", "--ext=ts", denoFenceScript},
		Resources:       denoFenceResources(tier),
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &noEscalation,
			ReadOnlyRootFilesystem:   &readOnlyRoot,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

// denoFenceResources caps the fence at 100m/64Mi but requests no more than the
// runtime: the scheduler reserves the larger of an init container and the
// pod's containers, so a bigger request would grow every project's footprint.
func denoFenceResources(tier string) corev1.ResourceRequirements {
	runtime := denoResourceRequirements(tier).Requests
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(denoFenceCPU),
		corev1.ResourceMemory: resource.MustParse(denoFenceMemory),
	}
	requests := corev1.ResourceList{}
	for name, limit := range limits {
		requests[name] = limit
		if wanted := runtime[name]; wanted.Cmp(limit) < 0 {
			requests[name] = wanted
		}
	}
	return corev1.ResourceRequirements{Requests: requests, Limits: limits}
}

// withDenoFence returns the init containers with the wanted fence in place of
// any other, and whether that changed them.
func withDenoFence(initContainers []corev1.Container, want corev1.Container) ([]corev1.Container, bool) {
	out := make([]corev1.Container, 0, len(initContainers)+1)
	found, changed := false, false
	for _, container := range initContainers {
		if container.Name != denoFenceContainerName {
			out = append(out, container)
			continue
		}
		found = true
		if !apiequality.Semantic.DeepEqual(container.Resources, want.Resources) || container.Image != want.Image {
			changed = true
		}
		out = append(out, want)
	}
	if !found {
		return append(out, want), true
	}
	return out, changed
}
