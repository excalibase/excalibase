// Package dockerapps runs Containers (app hosting) on a single Docker or
// Podman host (EXC-575, ADR 0039). It applies the workload the Kubernetes
// renderer produces as containers, through the engine proxy: one network per
// project, the edge routing to the app's containers by name, named volumes for
// disks, and a probe binary exec'd inside each container for readiness.
package dockerapps

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/distribution/reference"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// ErrUnsupportedWorkload refuses a rendered workload this runtime cannot run as rendered.
var ErrUnsupportedWorkload = errors.New("the app's workload cannot run on a single host")

// probeSpec is the readiness check: an HTTP GET on a path, else a TCP connect.
type probeSpec struct {
	HTTP bool   `json:"http,omitempty"`
	Port int    `json:"port"`
	Path string `json:"path,omitempty"`
}

func (p probeSpec) args() []string {
	if p.HTTP {
		return []string{"http", fmt.Sprint(p.Port), p.Path}
	}
	return []string{"tcp", fmt.Sprint(p.Port)}
}

// registryAuth is the pull credential for the image's registry.
type registryAuth struct {
	Registry, Username, Password string
}

// containerSpec is one app deploy as containers: what every replica runs.
type containerSpec struct {
	Project, AppID, AppName, DeployID, Tier string
	Image                                   string
	Args, Env                               []string
	// Port is the HTTP port the edge routes to; 0 for an internal service.
	Port     int
	Probe    probeSpec
	Replicas int
	// Recreate stops the old containers before the new ones start: a disk admits one writer.
	Recreate                       bool
	NanoCPUs, MemoryBytes          int64
	CPURequestMilli, MemoryRequest int64
	DiskClaim, DiskMountPath       string
	Pull                           *registryAuth
}

// specFromWorkload reads what Kubernetes would run from the rendered objects;
// anything it cannot carry over is refused rather than dropped.
func specFromWorkload(workload *k8s.AppWorkload) (containerSpec, error) {
	if workload == nil || workload.Deployment == nil {
		return containerSpec{}, fmt.Errorf("%w: no deployment was rendered", ErrUnsupportedWorkload)
	}
	dep := workload.Deployment
	pod := dep.Spec.Template.Spec
	if len(pod.Containers) != 1 || len(pod.InitContainers) != 0 {
		return containerSpec{}, fmt.Errorf("%w: exactly one container is run", ErrUnsupportedWorkload)
	}
	container := pod.Containers[0]
	spec := containerSpec{
		Project: dep.Labels[k8s.AppLabelProject], AppID: dep.Labels[k8s.AppLabelApp],
		AppName: dep.Labels[k8s.AppLabelName], Tier: dep.Labels[k8s.AppLabelTier],
		DeployID: dep.Annotations[k8s.AppDeployAnnotation],
		Args:     append([]string(nil), container.Args...),
		Recreate: dep.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType,
	}
	for what, value := range map[string]string{"project": spec.Project, "app": spec.AppID, "app name": spec.AppName, "deploy": spec.DeployID} {
		if value == "" {
			return containerSpec{}, fmt.Errorf("%w: the workload names no %s", ErrUnsupportedWorkload, what)
		}
	}
	if dep.Spec.Replicas == nil {
		return containerSpec{}, fmt.Errorf("%w: the workload states no replica count", ErrUnsupportedWorkload)
	}
	spec.Replicas = int(*dep.Spec.Replicas)
	steps := []func(*containerSpec) error{
		func(s *containerSpec) (err error) { s.Image, err = qualifiedImage(container.Image); return err },
		func(s *containerSpec) (err error) {
			s.Env, err = containerEnv(container.Env, workload.EnvSecret)
			return err
		},
		func(s *containerSpec) error { return s.readPorts(container, workload.Internal) },
		func(s *containerSpec) error { return s.readResources(container.Resources) },
		func(s *containerSpec) error { return s.readDisk(workload.Disk, container.VolumeMounts) },
		func(s *containerSpec) (err error) { s.Pull, err = pullAuth(workload.PullSecret); return err },
	}
	for _, step := range steps {
		if err := step(&spec); err != nil {
			return containerSpec{}, err
		}
	}
	return spec, nil
}

// qualifiedImage names the registry: Podman will not guess one for a short name.
func qualifiedImage(image string) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("%w: image %q: %v", ErrUnsupportedWorkload, image, err)
	}
	return named.String(), nil
}

func containerEnv(vars []corev1.EnvVar, secret *corev1.Secret) ([]string, error) {
	env := make([]string, 0, len(vars))
	for _, variable := range vars {
		if variable.ValueFrom == nil {
			env = append(env, variable.Name+"="+variable.Value)
			continue
		}
		ref := variable.ValueFrom.SecretKeyRef
		if ref == nil || secret == nil || ref.Name != secret.Name {
			return nil, fmt.Errorf("%w: variable %q is not read from the app's own secret", ErrUnsupportedWorkload, variable.Name)
		}
		value, ok := secret.Data[ref.Key]
		if !ok {
			return nil, fmt.Errorf("%w: variable %q has no value in the app's secret", ErrUnsupportedWorkload, variable.Name)
		}
		env = append(env, variable.Name+"="+string(value))
	}
	return env, nil
}

func (s *containerSpec) readPorts(container corev1.Container, internal bool) error {
	for _, port := range container.Ports {
		if port.Name == k8s.AppHTTPPortName {
			s.Port = int(port.ContainerPort)
		}
	}
	if !internal && s.Port == 0 {
		return fmt.Errorf("%w: a public app names no HTTP port", ErrUnsupportedWorkload)
	}
	if internal {
		s.Port = 0
	}
	probe := container.ReadinessProbe
	switch {
	case probe != nil && probe.HTTPGet != nil:
		s.Probe = probeSpec{HTTP: true, Port: probe.HTTPGet.Port.IntValue(), Path: probe.HTTPGet.Path}
	case probe != nil && probe.TCPSocket != nil:
		s.Probe = probeSpec{Port: probe.TCPSocket.Port.IntValue()}
	default:
		return fmt.Errorf("%w: the workload has no readiness check", ErrUnsupportedWorkload)
	}
	if s.Probe.Port < 1 || s.Probe.Port > 65535 || (s.Probe.HTTP && !strings.HasPrefix(s.Probe.Path, "/")) {
		return fmt.Errorf("%w: the readiness check %+v cannot be run", ErrUnsupportedWorkload, s.Probe)
	}
	return nil
}

func (s *containerSpec) readResources(resources corev1.ResourceRequirements) error {
	cpu, memory := resources.Limits.Cpu(), resources.Limits.Memory()
	if cpu.IsZero() || memory.IsZero() {
		return fmt.Errorf("%w: the workload has no CPU or memory limit", ErrUnsupportedWorkload)
	}
	s.NanoCPUs = cpu.MilliValue() * 1_000_000
	s.MemoryBytes = memory.Value()
	s.CPURequestMilli = resources.Requests.Cpu().MilliValue()
	s.MemoryRequest = resources.Requests.Memory().Value()
	return nil
}

func (s *containerSpec) readDisk(claim *corev1.PersistentVolumeClaim, mounts []corev1.VolumeMount) error {
	if claim == nil {
		if len(mounts) != 0 {
			return fmt.Errorf("%w: a volume is mounted without a disk", ErrUnsupportedWorkload)
		}
		return nil
	}
	if len(mounts) != 1 || mounts[0].MountPath == "" {
		return fmt.Errorf("%w: the disk is not mounted once", ErrUnsupportedWorkload)
	}
	s.DiskClaim, s.DiskMountPath = claim.Name, mounts[0].MountPath
	return nil
}

// pullAuth reads the one registry login back out of the rendered pull secret.
func pullAuth(secret *corev1.Secret) (*registryAuth, error) {
	if secret == nil {
		return nil, nil
	}
	var config struct {
		Auths map[string]struct{ Username, Password string } `json:"auths"`
	}
	if err := json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config); err != nil {
		return nil, fmt.Errorf("%w: the pull secret cannot be read", ErrUnsupportedWorkload)
	}
	for registry, login := range config.Auths {
		if strings.Contains(registry, "://") {
			continue // Docker Hub's legacy key; the plain host carries the same login
		}
		return &registryAuth{Registry: registry, Username: login.Username, Password: login.Password}, nil
	}
	return nil, fmt.Errorf("%w: the pull secret names no registry", ErrUnsupportedWorkload)
}

// header is the X-Registry-Auth value the engine pulls with.
func (a *registryAuth) header() string {
	if a == nil {
		return ""
	}
	encoded, _ := json.Marshal(map[string]string{"username": a.Username, "password": a.Password, "serveraddress": a.Registry})
	return base64.URLEncoding.EncodeToString(encoded)
}
