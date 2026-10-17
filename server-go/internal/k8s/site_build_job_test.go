package k8s

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

func testBuildOptions() SiteBuildOptions {
	return SiteBuildOptions{
		Namespace: "site-builds", RuntimeClass: "gvisor", PriorityClass: "site-build-low", Slots: 2,
		PrepareImage: "busybox:1.37.0",
	}
}

func testRunSpec() sitebuilds.RunSpec {
	return sitebuilds.RunSpec{
		BuildID: "0b6f1c2e-6a7b-4c1d-9e2f-3a4b5c6d7e8f", SiteID: "site-1", ProjectID: "proj-1",
		Image: "node:22-bookworm", CloneURL: "https://github.com/acme/site.git", Commit: strings.Repeat("a", 40),
		CloneToken: "ghs_token", RootDir: "web", OutputDir: "dist", BuildCommand: "npm run build:prod",
		CPUMillis: 2000, Memory: "2Gi", Disk: "5Gi", Timeout: 10 * time.Minute,
		Env:         map[string]string{"PUBLIC_API_URL": "https://api.example.com"},
		LargestPlan: sitebuilds.BuildSize{CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi"},
	}
}

func containerNamed(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, container := range containers {
		if container.Name == name {
			return container
		}
	}
	t.Fatalf("no container %q", name)
	return corev1.Container{}
}

// expectAll reports every check that does not hold, by its message.
func expectAll(t *testing.T, checks map[string]bool) {
	t.Helper()
	for message, holds := range checks {
		if !holds {
			t.Error(message)
		}
	}
}

func TestSiteBuildJob_MatchesTheSpikeSpec(t *testing.T) {
	job := renderSiteBuildJob(testBuildOptions(), testRunSpec())
	spec := job.Spec
	expectAll(t, map[string]bool{
		"job name and namespace":                           job.Namespace == "site-builds" && job.Name == "site-build-0b6f1c2e-6a7b-4c1d-9e2f-3a4b5c6d7e8f",
		"a failed build must not be retried":               spec.BackoffLimit != nil && *spec.BackoffLimit == 0,
		"deadline is the tier's 600 s":                     spec.ActiveDeadlineSeconds != nil && *spec.ActiveDeadlineSeconds == 600,
		"finished builds are cleaned up in 600 s":          spec.TTLSecondsAfterFinished != nil && *spec.TTLSecondsAfterFinished == 600,
		"the job starts suspended until its Secrets exist": spec.Suspend != nil && *spec.Suspend,
	})
}

func TestSiteBuildPod_MatchesTheSpikeSpec(t *testing.T) {
	pod := renderSiteBuildJob(testBuildOptions(), testRunSpec()).Spec.Template.Spec
	security := pod.SecurityContext
	order := []string{}
	for _, init := range pod.InitContainers {
		order = append(order, init.Name)
	}
	expectAll(t, map[string]bool{
		"builds run under gVisor":                     pod.RuntimeClassName != nil && *pod.RuntimeClassName == "gvisor",
		"low priority, never restarted":               pod.PriorityClassName == "site-build-low" && pod.RestartPolicy == corev1.RestartPolicyNever,
		"no service-account token in a build pod":     pod.AutomountServiceAccountToken != nil && !*pod.AutomountServiceAccountToken,
		"no service links in a build pod":             pod.EnableServiceLinks != nil && !*pod.EnableServiceLinks,
		"pod runs as 1000:1000, non-root, seccomp on": podLockedDown(security),
		"init order":                    slices.Equal(order, []string{"prepare-workdir", "wait-for-fence", "clone", "build"}),
		"collect is the only container": len(pod.Containers) == 1 && pod.Containers[0].Name == "collect",
	})
}

func podLockedDown(security *corev1.PodSecurityContext) bool {
	return security != nil && *security.RunAsNonRoot && *security.RunAsUser == 1000 && *security.RunAsGroup == 1000 &&
		*security.FSGroup == 1000 && security.SeccompProfile != nil && security.SeccompProfile.Type == corev1.SeccompProfileTypeRuntimeDefault
}

func TestSiteBuildJob_PrepareWorkdirIsRootWithNoCapabilities(t *testing.T) {
	pod := renderSiteBuildJob(testBuildOptions(), testRunSpec()).Spec.Template.Spec
	prepare := containerNamed(t, pod.InitContainers, "prepare-workdir")
	if !slices.Equal(prepare.Command, []string{"chmod", "1777", "/work", "/tmp"}) {
		t.Errorf("command %v", prepare.Command)
	}
	security := prepare.SecurityContext
	if *security.RunAsUser != 0 || *security.RunAsNonRoot || *security.AllowPrivilegeEscalation || !*security.ReadOnlyRootFilesystem ||
		!slices.Equal(security.Capabilities.Drop, []corev1.Capability{"ALL"}) || len(security.Capabilities.Add) != 0 {
		t.Errorf("prepare-workdir security %+v", security)
	}
}

func TestSiteBuildJob_EveryOtherStepIsLockedDown(t *testing.T) {
	pod := renderSiteBuildJob(testBuildOptions(), testRunSpec()).Spec.Template.Spec
	for _, container := range append(append([]corev1.Container{}, pod.InitContainers[1:]...), pod.Containers...) {
		security := container.SecurityContext
		if security == nil || *security.AllowPrivilegeEscalation || !*security.ReadOnlyRootFilesystem ||
			!slices.Equal(security.Capabilities.Drop, []corev1.Capability{"ALL"}) || security.RunAsUser != nil {
			t.Errorf("%s security %+v", container.Name, security)
		}
		if container.Resources.Requests.Cpu().Cmp(*container.Resources.Limits.Cpu()) != 0 ||
			container.Resources.Requests.Memory().Cmp(*container.Resources.Limits.Memory()) != 0 {
			t.Errorf("%s: requests must equal limits (Guaranteed QoS)", container.Name)
		}
	}
}

func TestSiteBuildJob_BuildGetsTheTierLimits(t *testing.T) {
	pod := renderSiteBuildJob(testBuildOptions(), testRunSpec()).Spec.Template.Spec
	build := containerNamed(t, pod.InitContainers, "build")
	limits := build.Resources.Limits
	if !limits.Cpu().Equal(resource.MustParse("2")) || !limits.Memory().Equal(resource.MustParse("2Gi")) ||
		!limits.StorageEphemeral().Equal(resource.MustParse("5Gi")) {
		t.Errorf("build limits %v", limits)
	}
	if !build.Resources.Requests.Cpu().Equal(resource.MustParse("2")) {
		t.Errorf("build requests %v", build.Resources.Requests)
	}
	for _, volume := range pod.Volumes {
		if volume.Name == "work" && (volume.EmptyDir == nil || !volume.EmptyDir.SizeLimit.Equal(resource.MustParse("5Gi"))) {
			t.Errorf("work volume %+v", volume)
		}
		if volume.Name == "tmp" && (volume.EmptyDir == nil || !volume.EmptyDir.SizeLimit.Equal(resource.MustParse("1Gi"))) {
			t.Errorf("tmp volume %+v", volume)
		}
	}
}

func TestSiteBuildJob_OnlyCloneHoldsTheGitTokenAndOnlyBuildTheEnv(t *testing.T) {
	pod := renderSiteBuildJob(testBuildOptions(), testRunSpec()).Spec.Template.Spec
	all := append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...)
	for _, container := range all {
		mountsToken := slices.ContainsFunc(container.VolumeMounts, func(m corev1.VolumeMount) bool { return m.Name == "git-token" })
		if mountsToken != (container.Name == "clone") {
			t.Errorf("%s mounts the git token: %v", container.Name, mountsToken)
		}
		usesEnv := len(container.EnvFrom) > 0
		if usesEnv != (container.Name == "build") {
			t.Errorf("%s reads the build env Secret: %v", container.Name, usesEnv)
		}
	}
	build := containerNamed(t, pod.InitContainers, "build")
	if build.EnvFrom[0].SecretRef == nil || build.EnvFrom[0].SecretRef.Name != "site-build-0b6f1c2e-6a7b-4c1d-9e2f-3a4b5c6d7e8f-env" {
		t.Errorf("env from %+v", build.EnvFrom)
	}
	env := map[string]string{}
	for _, variable := range build.Env {
		env[variable.Name] = variable.Value
	}
	if env["HOME"] != "/work/home" || env["CI"] != "true" || env["ROOT_DIR"] != "web" || env["BUILD_COMMAND"] != "npm run build:prod" ||
		env["npm_config_cache"] != "/work/npm-cache" || env["NEXT_TELEMETRY_DISABLED"] != "1" || env["ASTRO_TELEMETRY_DISABLED"] != "1" {
		t.Errorf("build env %v", env)
	}
	if _, leaked := env["PUBLIC_API_URL"]; leaked {
		t.Error("declared variables travel in the Secret, not in the pod spec")
	}
	clone := containerNamed(t, pod.InitContainers, "clone")
	for _, variable := range clone.Env {
		if strings.Contains(variable.Value, "ghs_token") {
			t.Error("the token must not be in the pod spec")
		}
	}
}

func TestSiteBuildJob_CollectIsReadyOnlyOnceTheArchiveIsPacked(t *testing.T) {
	pod := renderSiteBuildJob(testBuildOptions(), testRunSpec()).Spec.Template.Spec
	collect := pod.Containers[0]
	probe := collect.ReadinessProbe
	if probe == nil || probe.Exec == nil || !slices.Equal(probe.Exec.Command, []string{"test", "-f", "/work/collect/ready"}) {
		t.Fatalf("readiness probe %+v", probe)
	}
}

func TestSiteBuildSecrets(t *testing.T) {
	spec := testRunSpec()
	env, git := renderSiteBuildSecrets(testBuildOptions(), spec)
	if env.Name != "site-build-0b6f1c2e-6a7b-4c1d-9e2f-3a4b5c6d7e8f-env" || env.StringData["PUBLIC_API_URL"] != "https://api.example.com" || len(env.StringData) != 1 {
		t.Errorf("env secret %+v", env)
	}
	if git.Name != "site-build-0b6f1c2e-6a7b-4c1d-9e2f-3a4b5c6d7e8f-git" || git.StringData["token"] != "ghs_token" {
		t.Errorf("git secret %+v", git.Name)
	}
}

func TestSiteBuildEgressPolicy_InternetOnly(t *testing.T) {
	policy := renderSiteBuildEgressPolicy(testBuildOptions())
	if policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"] != "site-build" {
		t.Errorf("selector %v", policy.Spec.PodSelector)
	}
	if len(policy.Spec.Ingress) != 0 || !slices.Contains(policy.Spec.PolicyTypes, "Ingress") || !slices.Contains(policy.Spec.PolicyTypes, "Egress") {
		t.Errorf("a build takes no ingress at all: %+v", policy.Spec)
	}
	var v4Except []string
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil && peer.IPBlock.CIDR == "0.0.0.0/0" {
				v4Except = peer.IPBlock.Except
			}
		}
	}
	for _, private := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10", "127.0.0.0/8"} {
		if !slices.Contains(v4Except, private) {
			t.Errorf("%s is not excluded from the internet rule", private)
		}
	}
}

func TestSiteBuildQuota_IsTheSlotBackstop(t *testing.T) {
	quota := renderSiteBuildQuota(testBuildOptions(), testRunSpec().LargestPlan)
	hard := quota.Spec.Hard
	cpu, memory := hard[corev1.ResourceRequestsCPU], hard[corev1.ResourceRequestsMemory]
	cpuLimit, memoryLimit := hard[corev1.ResourceLimitsCPU], hard[corev1.ResourceLimitsMemory]
	if !hard.Pods().Equal(resource.MustParse("2")) || !cpu.Equal(resource.MustParse("4")) || !memory.Equal(resource.MustParse("8Gi")) ||
		!cpuLimit.Equal(resource.MustParse("4")) || !memoryLimit.Equal(resource.MustParse("8Gi")) {
		t.Errorf("quota %v", hard)
	}
	if storage := hard[corev1.ResourceRequestsEphemeralStorage]; !storage.Equal(resource.MustParse("20Gi")) {
		t.Errorf("ephemeral storage %v", storage)
	}
}

// The API server refuses an except range that is not a strict subset of its CIDR
// (an IPv4-mapped IPv6 range counts as IPv4); the live build test caught one.
func TestSiteBuildEgressExceptsAreStrictSubsets(t *testing.T) {
	policy := renderSiteBuildEgressPolicy(SiteBuildOptions{Namespace: "builds", ExtraDenyCIDRs: []string{"203.0.113.10/32", "2001:db8::/32"}})
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil {
				checkStrictSubsets(t, peer.IPBlock.CIDR, peer.IPBlock.Except)
			}
		}
	}
	if len(policy.Spec.Egress) != 2 || len(policy.Spec.Egress[1].To) != 1 || policy.Spec.Egress[1].To[0].IPBlock.CIDR != "0.0.0.0/0" {
		t.Errorf("builds reach the IPv4 internet only, as apps do: %+v", policy.Spec.Egress)
	}
}

func checkStrictSubsets(t *testing.T, cidr string, excepts []string) {
	t.Helper()
	outer := netip.MustParsePrefix(cidr)
	for _, except := range excepts {
		inner, err := netip.ParsePrefix(except)
		if err != nil {
			t.Fatalf("%s: %v", except, err)
		}
		inner = netip.PrefixFrom(inner.Addr().Unmap(), inner.Bits()-unmappedBits(inner))
		if inner.Addr().Is4() != outer.Addr().Is4() || inner.Bits() <= outer.Bits() || !outer.Contains(inner.Addr()) {
			t.Errorf("except %s is not a strict subset of %s", except, cidr)
		}
	}
}

func unmappedBits(prefix netip.Prefix) int {
	if prefix.Addr().Is4In6() {
		return 96
	}
	return 0
}

// Builds fetch packages over HTTP(S) only: no SMTP spam from the platform's IPs,
// and the install's extra denied ranges (its own public IPs) apply as to apps.
func TestSiteBuildEgressIsWebOnlyAndHonoursExtraDenies(t *testing.T) {
	policy := renderSiteBuildEgressPolicy(SiteBuildOptions{Namespace: "builds", ExtraDenyCIDRs: []string{"203.0.113.10/32", "2001:db8::/32"}})
	internet := policy.Spec.Egress[1]
	var ports []string
	for _, port := range internet.Ports {
		ports = append(ports, string(*port.Protocol)+"/"+port.Port.String())
	}
	if !slices.Equal(ports, []string{"TCP/80", "TCP/443"}) {
		t.Errorf("internet ports %v", ports)
	}
	except := internet.To[0].IPBlock.Except
	if !slices.Contains(except, "203.0.113.10/32") || slices.Contains(except, "2001:db8::/32") {
		t.Errorf("except %v: the extra IPv4 deny applies, an IPv6 one has no IPv4 block to sit in", except)
	}
}

// The root chmod step runs a minimal pinned image, never the user-chosen Node image.
func TestSiteBuildPrepareStepRunsThePrepareImage(t *testing.T) {
	options := testBuildOptions()
	options.PrepareImage = "busybox:1.37.0@sha256:" + strings.Repeat("b", 64)
	pod := renderSiteBuildJob(options, testRunSpec()).Spec.Template.Spec
	for _, container := range pod.InitContainers {
		if container.Name == "prepare-workdir" && container.Image != options.PrepareImage {
			t.Errorf("prepare runs %s", container.Image)
		}
	}
}
