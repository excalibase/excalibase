package dockerapps

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

type stubResolver struct{}

func (stubResolver) ResolveReference(string, apphost.ResolvedReference) (string, error) {
	return "postgresql://owner:pw@excalibase-proj-abc-postgres:5432/app?sslmode=disable", nil
}

func (stubResolver) ResolveSecret(string, apphost.SecretRef) (string, error) {
	return "sk_live_secret", nil
}

func literal(value string) *string { return &value }

func testApp() *apphost.App {
	return &apphost.App{
		ID: "app-01", ProjectID: "proj-abc", Name: "web", Image: "busybox:1.37",
		Args: []string{"httpd", "-f", "-p", "8080"}, Port: 8080, Replicas: 2, Tier: domain.Standard,
		HealthCheckPath: "/health", Status: apphost.StatusCreated,
	}
}

// renderOptions are what the single host renders with: its sandbox name and
// placeholders for the Kubernetes-only route fields.
func renderOptions() k8s.AppRenderOptions {
	opts := RenderOptions("apps.example.com", "runsc")
	opts.DeployID, opts.EnvRevision = "dep-1", "1"
	return opts
}

func render(t *testing.T, app *apphost.App, opts k8s.AppRenderOptions) *k8s.AppWorkload {
	t.Helper()
	workload, err := k8s.RenderAppWorkload("4f1c9a0e", app, stubResolver{}, opts)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return workload
}

func TestSpecFromWorkloadCarriesWhatKubernetesWouldRun(t *testing.T) {
	app := testApp()
	app.Env = []apphost.EnvVar{
		{Name: "MODE", Kind: apphost.KindLiteral, Value: literal("one")},
		{Name: "API_KEY", Kind: apphost.KindSecret, Secret: func() *apphost.SecretRef {
			ref := apphost.AppSecretRef(app.ProjectID, app.ID, "API_KEY")
			return &ref
		}()},
	}
	spec, err := specFromWorkload(render(t, app, renderOptions()))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Project != "proj-abc" || spec.AppID != "app-01" || spec.AppName != "web" || spec.DeployID != "dep-1" || spec.Tier != "standard" {
		t.Fatalf("identity = %+v", spec)
	}
	if spec.Image != "docker.io/library/busybox:1.37" {
		t.Fatalf("image %q is not fully qualified", spec.Image)
	}
	if !slices.Equal(spec.Args, app.Args) || spec.Replicas != 2 || spec.Port != 8080 {
		t.Fatalf("args %v replicas %d port %d", spec.Args, spec.Replicas, spec.Port)
	}
	if !slices.Contains(spec.Env, "MODE=one") || !slices.Contains(spec.Env, "API_KEY=sk_live_secret") {
		t.Fatalf("env %v misses the literal or the secret", spec.Env)
	}
	if spec.Probe != (probeSpec{HTTP: true, Port: 8080, Path: "/health"}) {
		t.Fatalf("probe %+v", spec.Probe)
	}
	if spec.NanoCPUs != 1_000_000_000 || spec.MemoryBytes != 1<<30 || spec.CPURequestMilli != 250 || spec.MemoryRequest != 512<<20 {
		t.Fatalf("standard tier resources %+v", spec)
	}
	if spec.Recreate || spec.DiskClaim != "" || spec.Pull != nil {
		t.Fatalf("no disk and no credential expected: %+v", spec)
	}
}

func TestSpecFromWorkloadMountsTheDiskAndRecreates(t *testing.T) {
	app := testApp()
	app.Replicas = 1
	app.Disk = &apphost.AppDisk{MountPath: "/data", Size: "1Gi", Generation: 2}
	spec, err := specFromWorkload(render(t, app, renderOptions()))
	if err != nil {
		t.Fatal(err)
	}
	if spec.DiskClaim != k8s.AppDiskClaimName("app-01", 2) || spec.DiskMountPath != "/data" || !spec.Recreate {
		t.Fatalf("disk %q at %q recreate %v", spec.DiskClaim, spec.DiskMountPath, spec.Recreate)
	}
}

func TestSpecFromWorkloadProbesAnInternalServiceByTCP(t *testing.T) {
	app := testApp()
	app.Internal, app.HealthCheckPath, app.Port = true, "", 0
	app.InternalPorts = []apphost.InternalPort{{Port: 6379, Protocol: "TCP"}}
	spec, err := specFromWorkload(render(t, app, renderOptions()))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Port != 0 || spec.Probe != (probeSpec{Port: 6379}) {
		t.Fatalf("internal service port %d probe %+v", spec.Port, spec.Probe)
	}
}

func TestSpecFromWorkloadReadsThePullCredential(t *testing.T) {
	app := testApp()
	app.Image = "ghcr.io/acme/web@sha256:" + strings.Repeat("a", 64)
	opts := renderOptions()
	opts.PullAuth = &k8s.RegistryAuth{Registry: "ghcr.io", Username: "bot", Password: "token"}
	spec, err := specFromWorkload(render(t, app, opts))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Pull == nil || *spec.Pull != (registryAuth{Registry: "ghcr.io", Username: "bot", Password: "token"}) {
		t.Fatalf("pull %+v", spec.Pull)
	}
	raw, err := base64.URLEncoding.DecodeString(spec.Pull.header())
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]string
	if err := json.Unmarshal(raw, &header); err != nil || header["serveraddress"] != "ghcr.io" || header["username"] != "bot" {
		t.Fatalf("X-Registry-Auth %s (%v)", raw, err)
	}
}

func TestSpecFromWorkloadRefusesWhatItCannotCarry(t *testing.T) {
	cases := map[string]func(*k8s.AppWorkload){
		"no deploy id": func(w *k8s.AppWorkload) { w.Deployment.Annotations = nil },
		"no probe":     func(w *k8s.AppWorkload) { w.Deployment.Spec.Template.Spec.Containers[0].ReadinessProbe = nil },
		"no limits": func(w *k8s.AppWorkload) {
			w.Deployment.Spec.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
		},
		"second process": func(w *k8s.AppWorkload) {
			pod := &w.Deployment.Spec.Template.Spec
			pod.Containers = append(pod.Containers, pod.Containers[0])
		},
		"foreign secret": func(w *k8s.AppWorkload) {
			c := &w.Deployment.Spec.Template.Spec.Containers[0]
			c.Env = append(c.Env, corev1.EnvVar{Name: "X", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "platform"}, Key: "k"}}})
		},
		"field ref": func(w *k8s.AppWorkload) {
			c := &w.Deployment.Spec.Template.Spec.Containers[0]
			c.Env = append(c.Env, corev1.EnvVar{Name: "NODE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			workload := render(t, testApp(), renderOptions())
			mutate(workload)
			if _, err := specFromWorkload(workload); !errors.Is(err, ErrUnsupportedWorkload) {
				t.Fatalf("err = %v, want ErrUnsupportedWorkload", err)
			}
		})
	}
}
