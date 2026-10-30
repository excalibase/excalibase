package k8s

import (
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

func testAppBuildOptions() AppBuildOptions {
	return AppBuildOptions{
		Namespace: "app-builds", RuntimeClass: "gvisor", PriorityClass: "build-low", Slots: 1,
		PrepareImage: "busybox:1.37.0", ToolImage: "node:22-bookworm", BuildKitImage: "moby/buildkit:v0.34.0-rootless",
		BuilderImage: "paketobuildpacks/ubuntu-noble-builder:0.0.203", RunImage: "docker.io/paketobuildpacks/ubuntu-noble-run:0.0.150@sha256:feed",
		CraneImage: "gcr.io/go-containerregistry/crane:v0.20.6",
	}
}

func testAppRunSpec(builder appbuilds.Builder) appbuilds.RunSpec {
	return appbuilds.RunSpec{
		BuildID: "0b6f1c2e-6a7b-4c1d-9e2f-3a4b5c6d7e8f", AppID: "app-1", ProjectID: "proj-1", Builder: builder,
		CloneURL: "https://github.com/acme/api.git", Commit: strings.Repeat("a", 40), CloneToken: "ghs_token",
		RootDir: "api", BuildArgs: map[string]string{"NODE_ENV": "production"}, Secrets: map[string]string{"NPM_TOKEN": "s3cret-value"},
		CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", Timeout: 15 * time.Minute, MaxImageBytes: 2 << 30,
		LargestPlan: sitebuilds.BuildSize{CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi"},
	}
}

func initNames(pod corev1.PodSpec) []string {
	names := []string{}
	for _, init := range pod.InitContainers {
		names = append(names, init.Name)
	}
	return names
}

func TestAppBuildPod_RunsUnderGVisorWithoutAnyPlatformCredential(t *testing.T) {
	for _, builder := range []appbuilds.Builder{appbuilds.BuilderDockerfile, appbuilds.BuilderBuildpacks} {
		job := renderAppBuildJob(testAppBuildOptions(), testAppRunSpec(builder))
		pod := job.Spec.Template.Spec
		expectAll(t, map[string]bool{
			string(builder) + ": job named for the build":           job.Name == "app-build-0b6f1c2e-6a7b-4c1d-9e2f-3a4b5c6d7e8f" && job.Namespace == "app-builds",
			string(builder) + ": deadline is the plan's 15 minutes": *job.Spec.ActiveDeadlineSeconds == 900,
			string(builder) + ": never retried, starts suspended":   *job.Spec.BackoffLimit == 0 && *job.Spec.Suspend,
			string(builder) + ": gVisor":                            *pod.RuntimeClassName == "gvisor",
			string(builder) + ": low priority":                      pod.PriorityClassName == "build-low",
			string(builder) + ": no service-account token":          !*pod.AutomountServiceAccountToken,
			string(builder) + ": no service links":                  !*pod.EnableServiceLinks,
			string(builder) + ": pod non-root, seccomp on":          podLockedDown(pod.SecurityContext),
			string(builder) + ": fence before any user code":        slices.Equal(initNames(pod)[:3], []string{"prepare-workdir", "wait-for-fence", "clone"}),
			string(builder) + ": no privileged container":           noPrivileged(pod),
			string(builder) + ": app label":                         job.Labels[appBuildLabelApp] == "app-1",
		})
		for _, container := range append(pod.InitContainers, pod.Containers...) {
			if container.Name != siteBuildClone && mounts(container, "git-token") {
				t.Errorf("%s: %s holds the clone token", builder, container.Name)
			}
			if userCode(container.Name) && mounts(container, "git-token") {
				t.Errorf("%s: user code in %s can read the clone token", builder, container.Name)
			}
			for _, env := range container.Env {
				if strings.Contains(env.Value, "s3cret-value") || strings.Contains(env.Value, "ghs_token") {
					t.Errorf("%s: %s carries a secret in its env", builder, container.Name)
				}
			}
			if strings.Contains(strings.Join(append(container.Command, container.Args...), " "), "s3cret-value") {
				t.Errorf("%s: %s carries a secret on its command line", builder, container.Name)
			}
		}
	}
}

func userCode(container string) bool {
	return container == appBuildBuildKit || container == appBuildBuildpacks
}

func mounts(container corev1.Container, volume string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.Name == volume {
			return true
		}
	}
	return false
}

// noPrivileged: no privileged container, and no capability beyond newuidmap's two for BuildKit.
func noPrivileged(pod corev1.PodSpec) bool {
	for _, container := range append(pod.InitContainers, pod.Containers...) {
		security := container.SecurityContext
		if security != nil && security.Privileged != nil && *security.Privileged {
			return false
		}
		if security != nil && security.Capabilities != nil && len(security.Capabilities.Add) > 0 && container.Name != appBuildBuildKit {
			return false
		}
	}
	return true
}

// The spike's passing Dockerfile builder: rootless BuildKit, daemonless, inside the sandbox.
func TestAppBuildPod_DockerfileBuildsWithRootlessBuildKit(t *testing.T) {
	pod := renderAppBuildJob(testAppBuildOptions(), testAppRunSpec(appbuilds.BuilderDockerfile)).Spec.Template.Spec
	build := containerNamed(t, pod.InitContainers, appBuildBuildKit)
	command := strings.Join(append(build.Command, build.Args...), " ")
	security := build.SecurityContext
	expectAll(t, map[string]bool{
		"steps in order":                       slices.Equal(initNames(pod), []string{"prepare-workdir", "wait-for-fence", "clone", "buildkit"}),
		"rootless BuildKit image":              build.Image == "moby/buildkit:v0.34.0-rootless",
		"a missing Dockerfile is named":        strings.Contains(command, "exit 12") && strings.Contains(command, "/work/src/api/Dockerfile"),
		"daemonless":                           strings.Contains(command, "exec buildctl-daemonless.sh") && build.Args[0] == "build",
		"context is the root dir":              strings.Contains(command, "context=/work/src/api"),
		"plain build arg":                      strings.Contains(command, "build-arg:NODE_ENV=production"),
		"secret as a mount, never a build arg": strings.Contains(command, "id=NPM_TOKEN,src=/run/build-secrets/NPM_TOKEN") && !strings.Contains(command, "build-arg:NPM_TOKEN"),
		"OCI archive output":                   strings.Contains(command, "type=oci,dest=/work/out/image.tar"),
		"state on the work disk":               envValue(build, "BUILDKITD_FLAGS") == "--oci-worker-no-process-sandbox --root /work/buildkit",
		"the pod's non-root user":              security.RunAsUser == nil && security.Privileged == nil,
		"no seccomp or AppArmor opt-out":       security.SeccompProfile == nil && security.AppArmorProfile == nil,
		"plan CPU, memory and disk":            build.Resources.Limits.Memory().String() == "4Gi" && build.Resources.Limits.StorageEphemeral().String() == "10Gi" && build.Resources.Limits.Cpu().MilliValue() == 2000,
		"reads the secrets as files":           mounts(build, "build-secrets"),
	})
}

// Buildpacks run the kpack way: the platform's phases apart from the two that run
// the app's code, exporting to a layout, so no step holds a registry credential.
func TestAppBuildPod_BuildpacksKeepTheAppsCodeInItsOwnStep(t *testing.T) {
	pod := renderAppBuildJob(testAppBuildOptions(), testAppRunSpec(appbuilds.BuilderBuildpacks)).Spec.Template.Spec
	want := []string{"prepare-workdir", "wait-for-fence", "clone", "share-checkout", "run-image", "analyze", "buildpacks", "export"}
	if got := initNames(pod); !slices.Equal(got, want) {
		t.Fatalf("steps %v, want %v", got, want)
	}
	fetch := containerNamed(t, pod.InitContainers, appBuildRunImage)
	if got := strings.Join(fetch.Args, " "); got != "pull --platform=linux/amd64 --format=oci docker.io/paketobuildpacks/ubuntu-noble-run:0.0.150@sha256:feed /work/out/layout/index.docker.io/paketobuildpacks/ubuntu-noble-run/0.0.150" {
		t.Errorf("run image fetch %q", got)
	}
	if collect := pod.Containers[0]; collect.SecurityContext.RunAsUser == nil || *collect.SecurityContext.RunAsUser != appBuildCNBUID {
		t.Errorf("the collect step reads the exporter's blobs as the CNB user")
	}
	share := containerNamed(t, pod.InitContainers, appBuildShareCheckout)
	if share.SecurityContext.RunAsUser != nil || strings.Join(share.Command, " ") != "chmod -R a+rwX /work/src" {
		t.Errorf("the checkout is opened by its owner, the pod user, with no capability: %v %v", share.Command, share.SecurityContext.RunAsUser)
	}
	scripts := map[string]string{}
	for _, phase := range append([]string{appBuildRunImage}, appBuildPhases...) {
		container := containerNamed(t, pod.InitContainers, phase)
		security := container.SecurityContext
		scripts[phase] = strings.Join(container.Command, " ")
		expectAll(t, map[string]bool{
			phase + ": as the CNB user":                        *security.RunAsUser == appBuildCNBUID,
			phase + ": locked down":                            *security.ReadOnlyRootFilesystem && !*security.AllowPrivilegeEscalation && len(security.Capabilities.Drop) == 1,
			phase + ": secrets only where the app's code runs": mounts(container, "build-secrets") == (phase == appBuildBuildpacks),
		})
	}
	expectAll(t, map[string]bool{
		"analyze alone, before detection":          strings.Contains(scripts["analyze"], "/cnb/lifecycle/analyzer -layers=/work/layers -run-image=docker.io/paketobuildpacks/ubuntu-noble-run:0.0.150 -layout") && !strings.Contains(scripts["analyze"], "restorer"),
		"detect, restore, then build the root dir": strings.Contains(scripts["buildpacks"], "/cnb/lifecycle/detector -layers=/work/layers -app=/work/src/api -platform=/run/platform") && strings.Contains(scripts["buildpacks"], "&& /cnb/lifecycle/restorer -layers=/work/layers -layout") && strings.Contains(scripts["buildpacks"], "&& /cnb/lifecycle/builder"),
		"export to the layout":                     strings.Contains(scripts["export"], "/cnb/lifecycle/exporter") && strings.Contains(scripts["export"], "-layout -layout-dir=/work/out/layout "+appBuildImageRef),
		"builder image":                            containerNamed(t, pod.InitContainers, "export").Image == "paketobuildpacks/ubuntu-noble-builder:0.0.203",
	})
	build := containerNamed(t, pod.InitContainers, appBuildBuildpacks)
	if build.Resources.Limits.Memory().String() != "4Gi" {
		t.Errorf("the app's build runs with the plan's memory: %v", build.Resources.Limits)
	}
	for _, mount := range build.VolumeMounts {
		if mount.Name == "build-secrets" && mount.MountPath != "/run/platform/env" {
			t.Errorf("build-visible secrets go to the platform env dir: %s", mount.MountPath)
		}
	}
}

func TestLayoutPath(t *testing.T) {
	cases := map[string]string{
		"paketobuildpacks/ubuntu-noble-run:0.0.150":   "index.docker.io/paketobuildpacks/ubuntu-noble-run/0.0.150",
		"docker.io/paketobuildpacks/ubuntu-noble-run": "index.docker.io/paketobuildpacks/ubuntu-noble-run/latest",
		"registry.local:5000/run:2@sha256:abc":        "registry.local:5000/run/2",
		"ghcr.io/acme/run:1":                          "ghcr.io/acme/run/1",
		"excalibase.local/app:build":                  "excalibase.local/app/build",
	}
	for ref, want := range cases {
		if got := layoutPath(ref); got != want {
			t.Errorf("%s: %q, want %q", ref, got, want)
		}
	}
}

func TestAppBuildSecrets_KeepTheCloneTokenApart(t *testing.T) {
	secrets, git := renderAppBuildSecrets(testAppBuildOptions(), testAppRunSpec(appbuilds.BuilderDockerfile))
	if secrets.StringData["NPM_TOKEN"] != "s3cret-value" || len(secrets.StringData) != 1 {
		t.Errorf("build secrets %v", secrets.StringData)
	}
	if git.StringData["token"] != "ghs_token" || len(git.StringData) != 1 {
		t.Errorf("git secret %v", git.StringData)
	}
}

func TestAppBuildEnvironment_FencesAndBoundsOnlyAppBuilds(t *testing.T) {
	policy := renderAppBuildEgressPolicy(testAppBuildOptions())
	quota := renderAppBuildQuota(testAppBuildOptions(), sitebuilds.BuildSize{CPUMillis: 4000, Memory: "8Gi", Disk: "20Gi"})
	hard := quota.Spec.Hard
	expectAll(t, map[string]bool{
		"the fence selects app build pods":    policy.Spec.PodSelector.MatchLabels[siteBuildComponentKey] == appBuildComponent,
		"the fence is the site builds' fence": len(policy.Spec.Egress) == 2 && len(policy.Spec.PolicyTypes) == 2,
		"named apart from the site objects":   policy.Name == appBuildEgressName && quota.Name == appBuildQuotaName,
		"one slot of the largest app build":   hard.Pods().Value() == 1 && hard.Name(corev1.ResourceRequestsMemory, "").String() == "8Gi" && hard.Name(corev1.ResourceRequestsCPU, "").MilliValue() == 4000,
	})
}

func envValue(container corev1.Container, name string) string {
	for _, env := range container.Env {
		if env.Name == name {
			return env.Value
		}
	}
	return ""
}
