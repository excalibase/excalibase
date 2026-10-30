package config

// AppBuildTierLimits bound one git build of an app on a plan (EXC-547). They
// are a separate allowance from what the project runs, so a push never has to
// stop a running service to build.
type AppBuildTierLimits struct {
	CPUMillis int
	// Memory and Disk are Kubernetes quantities; Disk bounds the build's work volume.
	Memory         string
	Disk           string
	TimeoutSeconds int
	// Concurrency is how many of one project's app builds run at once.
	Concurrency int
	// MaxImage is the largest built image the build may hand to a deploy.
	MaxImage string
}

func appBuildLimits(cpuMillis int, memory, disk string, timeoutSeconds, concurrency int, maxImage string) AppBuildTierLimits {
	return AppBuildTierLimits{CPUMillis: cpuMillis, Memory: memory, Disk: disk, TimeoutSeconds: timeoutSeconds,
		Concurrency: concurrency, MaxImage: maxImage}
}
