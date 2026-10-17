package k8s

import (
	"embed"
	"fmt"
	"net/netip"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// The build pod is the gVisor spike's (docs/spikes/2026-10-17-gvisor-static-builds.md):
// prepare-workdir → wait-for-fence → clone → build, then a collect container the
// controller reads the packed output from. Only clone holds the git token, only
// build reads the declared variables, and no step holds a platform credential.

//go:embed site_build_scripts/*.sh
var siteBuildScripts embed.FS

const (
	siteBuildComponent     = "site-build"
	siteBuildScriptsName   = "site-build-scripts"
	siteBuildEgressName    = "site-build-egress"
	siteBuildQuotaName     = "site-build-slots"
	siteBuildUID           = int64(1000)
	siteBuildTTLSeconds    = int32(600)
	siteBuildTmpSize       = "1Gi"
	siteBuildCollectReady  = "/work/collect/ready"
	siteBuildArchivePath   = "/work/collect/site.tar.gz"
	siteBuildLabelBuild    = "excalibase.io/build"
	siteBuildLabelSite     = "excalibase.io/site"
	siteBuildLabelProject  = "excalibase.io/project"
	siteBuildComponentKey  = "app.kubernetes.io/component"
	siteBuildContainerRoot = "prepare-workdir"
	siteBuildFence         = "wait-for-fence"
	siteBuildClone         = "clone"
	siteBuildBuild         = "build"
	siteBuildCollect       = "collect"
)

// SiteBuildOptions is where and how this install runs builds.
type SiteBuildOptions struct {
	Namespace     string
	RuntimeClass  string
	PriorityClass string
	// Slots is how many builds run at once across the install; the quota backstops it.
	Slots int
	// PrepareImage runs the root chmod of the work dirs.
	PrepareImage string
	// ExtraDenyCIDRs are the install's own off-cluster ranges denied to apps (APP_EGRESS_EXTRA_DENY_CIDRS).
	ExtraDenyCIDRs []string
}

func siteBuildName(buildID string) string { return "site-build-" + buildID }

func siteBuildLabels(spec sitebuilds.RunSpec) map[string]string {
	return map[string]string{
		siteBuildComponentKey: siteBuildComponent, siteBuildLabelBuild: spec.BuildID,
		siteBuildLabelSite: spec.SiteID, siteBuildLabelProject: spec.ProjectID,
	}
}

func lockedDown() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: pointerTo(false),
		ReadOnlyRootFilesystem:   pointerTo(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func guaranteed(cpu, memory string) corev1.ResourceRequirements {
	list := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}
	return corev1.ResourceRequirements{Requests: list, Limits: list.DeepCopy()}
}

func scriptMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: "scripts", MountPath: "/scripts", ReadOnly: true}
}

func workMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{{Name: "work", MountPath: "/work"}, {Name: "tmp", MountPath: "/tmp"}}
}

func renderSiteBuildJob(options SiteBuildOptions, spec sitebuilds.RunSpec) *batchv1.Job {
	labels := siteBuildLabels(spec)
	deadline := int64(spec.Timeout.Seconds())
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: siteBuildName(spec.BuildID), Namespace: options.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            pointerTo(int32(0)),
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: pointerTo(siteBuildTTLSeconds),
			// Resumed once the Secrets it reads exist, owned by the Job.
			Suspend: pointerTo(true),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       renderSiteBuildPod(options, spec),
			},
		},
	}
}

func renderSiteBuildPod(options SiteBuildOptions, spec sitebuilds.RunSpec) corev1.PodSpec {
	return corev1.PodSpec{
		RuntimeClassName:             &options.RuntimeClass,
		PriorityClassName:            options.PriorityClass,
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: pointerTo(false),
		EnableServiceLinks:           pointerTo(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: pointerTo(true), RunAsUser: pointerTo(siteBuildUID), RunAsGroup: pointerTo(siteBuildUID), FSGroup: pointerTo(siteBuildUID),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		InitContainers: []corev1.Container{
			prepareWorkdirContainer(options.PrepareImage), fenceContainer(spec), cloneContainer(spec), buildContainer(spec),
		},
		Containers: []corev1.Container{collectContainer(spec)},
		Volumes:    siteBuildVolumes(spec),
	}
}

// prepareWorkdirContainer: emptyDir roots are 0777 without the sticky bit, which
// @swc/core 1.16+ refuses. Root owns the mount, so chmod needs no capability; the
// only root step runs a minimal pinned image, never the build's own.
func prepareWorkdirContainer(image string) corev1.Container {
	security := lockedDown()
	security.RunAsUser, security.RunAsNonRoot = pointerTo(int64(0)), pointerTo(false)
	return corev1.Container{
		Name: siteBuildContainerRoot, Image: image, Command: []string{"chmod", "1777", "/work", "/tmp"},
		SecurityContext: security, Resources: guaranteed("100m", "32Mi"), VolumeMounts: workMounts(),
	}
}

func fenceContainer(spec sitebuilds.RunSpec) corev1.Container {
	return corev1.Container{
		Name: siteBuildFence, Image: spec.Image, Command: []string{"bash", "/scripts/fence.sh"},
		SecurityContext: lockedDown(), Resources: guaranteed("100m", "32Mi"), VolumeMounts: []corev1.VolumeMount{scriptMount()},
	}
}

func cloneContainer(spec sitebuilds.RunSpec) corev1.Container {
	return corev1.Container{
		Name: siteBuildClone, Image: spec.Image, Command: []string{"bash", "/scripts/clone.sh"},
		Env: []corev1.EnvVar{
			{Name: "CLONE_URL", Value: spec.CloneURL}, {Name: "COMMIT", Value: spec.Commit}, {Name: "HOME", Value: "/work/home"},
		},
		SecurityContext: lockedDown(), Resources: guaranteed("500m", "256Mi"),
		VolumeMounts: append(workMounts(), scriptMount(), corev1.VolumeMount{Name: "git-token", MountPath: "/run/git", ReadOnly: true}),
	}
}

func buildContainer(spec sitebuilds.RunSpec) corev1.Container {
	cpu := resource.NewMilliQuantity(int64(spec.CPUMillis), resource.DecimalSI)
	list := corev1.ResourceList{
		corev1.ResourceCPU: *cpu, corev1.ResourceMemory: resource.MustParse(spec.Memory),
		corev1.ResourceEphemeralStorage: resource.MustParse(spec.Disk),
	}
	return corev1.Container{
		Name: siteBuildBuild, Image: spec.Image, Command: []string{"bash", "/scripts/build.sh"}, WorkingDir: "/work",
		// Explicit env wins over the Secret's, so a declared variable cannot move HOME or the commands.
		Env: []corev1.EnvVar{
			{Name: "HOME", Value: "/work/home"}, {Name: "npm_config_cache", Value: "/work/npm-cache"},
			{Name: "COREPACK_HOME", Value: "/work/corepack"}, {Name: "COREPACK_ENABLE_DOWNLOAD_PROMPT", Value: "0"},
			{Name: "CI", Value: "true"}, {Name: "NEXT_TELEMETRY_DISABLED", Value: "1"}, {Name: "ASTRO_TELEMETRY_DISABLED", Value: "1"},
			{Name: "ROOT_DIR", Value: spec.RootDir}, {Name: "INSTALL_COMMAND", Value: spec.InstallCommand},
			{Name: "BUILD_COMMAND", Value: spec.BuildCommand},
		},
		EnvFrom:         []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: siteBuildName(spec.BuildID) + "-env"}}}},
		SecurityContext: lockedDown(),
		Resources:       corev1.ResourceRequirements{Requests: list, Limits: list.DeepCopy()},
		VolumeMounts:    append(workMounts(), scriptMount()),
	}
}

func collectContainer(spec sitebuilds.RunSpec) corev1.Container {
	return corev1.Container{
		Name: siteBuildCollect, Image: spec.Image, Command: []string{"bash", "/scripts/collect.sh"},
		Env:             []corev1.EnvVar{{Name: "ROOT_DIR", Value: spec.RootDir}, {Name: "OUTPUT_DIR", Value: spec.OutputDir}},
		SecurityContext: lockedDown(), Resources: guaranteed("250m", "256Mi"),
		VolumeMounts: append(workMounts(), scriptMount()),
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"test", "-f", siteBuildCollectReady}}},
			PeriodSeconds: 2,
		},
	}
}

func siteBuildVolumes(spec sitebuilds.RunSpec) []corev1.Volume {
	disk, tmp := resource.MustParse(spec.Disk), resource.MustParse(siteBuildTmpSize)
	name := siteBuildName(spec.BuildID)
	return []corev1.Volume{
		{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &disk}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &tmp}}},
		{Name: "scripts", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: siteBuildScriptsName}}}},
		{Name: "git-token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name + "-git"}}},
	}
}

// renderSiteBuildSecrets: the declared variables and build-visible secrets, and the clone token, apart.
func renderSiteBuildSecrets(options SiteBuildOptions, spec sitebuilds.RunSpec) (*corev1.Secret, *corev1.Secret) {
	labels := siteBuildLabels(spec)
	env := make(map[string]string, len(spec.Env))
	for name, value := range spec.Env {
		env[name] = value
	}
	name := siteBuildName(spec.BuildID)
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-env", Namespace: options.Namespace, Labels: labels}, StringData: env},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + "-git", Namespace: options.Namespace, Labels: labels},
			StringData: map[string]string{"token": spec.CloneToken}}
}

func renderSiteBuildScripts(namespace string) (*corev1.ConfigMap, error) {
	data := map[string]string{}
	for _, script := range []string{"fence.sh", "clone.sh", "build.sh", "collect.sh"} {
		body, err := siteBuildScripts.ReadFile("site_build_scripts/" + script)
		if err != nil {
			return nil, fmt.Errorf("read build script %s: %w", script, err)
		}
		data[script] = string(body)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: siteBuildScriptsName, Namespace: namespace, Labels: map[string]string{siteBuildComponentKey: siteBuildComponent}},
		Data:       data,
	}, nil
}

// Builds reach the internet over HTTP(S) only.
const (
	httpPortNumber  = 80
	httpsPortNumber = 443
)

// renderSiteBuildEgressPolicy: DNS, then HTTP(S) to the IPv4 internet minus private,
// link-local (cloud metadata), CGNAT, loopback and the install's extra denied ranges.
// Web ports only, so no SMTP; no IPv6, no ingress.
func renderSiteBuildEgressPolicy(options SiteBuildOptions) *networkingv1.NetworkPolicy {
	namespace := options.Namespace
	except := append([]string{}, appDeniedRanges...)
	for _, cidr := range options.ExtraDenyCIDRs {
		if prefix, err := netip.ParsePrefix(cidr); err == nil && prefix.Addr().Is4() {
			except = append(except, prefix.Masked().String())
		}
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: siteBuildEgressName, Namespace: namespace, Labels: map[string]string{siteBuildComponentKey: siteBuildComponent}},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{siteBuildComponentKey: siteBuildComponent}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
						PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
					}},
					Ports: []networkingv1.NetworkPolicyPort{udpPort(dnsPortNumber), tcpPort(dnsPortNumber)},
				},
				{
					To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: except}}},
					Ports: []networkingv1.NetworkPolicyPort{tcpPort(httpPortNumber), tcpPort(httpsPortNumber)},
				},
			},
		},
	}
}

func udpPort(n int) networkingv1.NetworkPolicyPort {
	udp := corev1.ProtocolUDP
	port := intstr.FromInt(n)
	return networkingv1.NetworkPolicyPort{Protocol: &udp, Port: &port}
}

// renderSiteBuildQuota backstops the dispatcher: even a bug in it cannot run
// more builds, or bigger ones, than the slots allow.
func renderSiteBuildQuota(options SiteBuildOptions, largest sitebuilds.BuildSize) *corev1.ResourceQuota {
	slots := int64(options.Slots)
	cpu := resource.NewMilliQuantity(slots*int64(largest.CPUMillis), resource.DecimalSI)
	memory := scaled(largest.Memory, slots)
	disk := scaled(largest.Disk, slots)
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: siteBuildQuotaName, Namespace: options.Namespace, Labels: map[string]string{siteBuildComponentKey: siteBuildComponent}},
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
			corev1.ResourcePods:                     resource.MustParse(strconv.FormatInt(slots, 10)),
			corev1.ResourceRequestsCPU:              *cpu,
			corev1.ResourceLimitsCPU:                *cpu,
			corev1.ResourceRequestsMemory:           memory,
			corev1.ResourceLimitsMemory:             memory,
			corev1.ResourceRequestsEphemeralStorage: disk,
		}},
	}
}

func scaled(quantity string, times int64) resource.Quantity {
	parsed := resource.MustParse(quantity)
	return *resource.NewQuantity(parsed.Value()*times, resource.BinarySI)
}
