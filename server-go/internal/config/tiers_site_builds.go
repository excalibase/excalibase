package config

// SiteBuildTierLimits bound one git build of a site on a plan (EXC-354). The
// starting numbers are the gVisor spike's: gVisor bills its overhead to the
// pod's own CPU, so every plan builds with at least 2 CPUs.
type SiteBuildTierLimits struct {
	CPUMillis int
	// Memory and Disk are Kubernetes quantities; Disk bounds the build's work volume.
	Memory         string
	Disk           string
	TimeoutSeconds int
	// Concurrency is how many of one project's builds run at once.
	Concurrency int
}

// MinSiteBuildCPUMillis is the least CPU a build gets: under gVisor less makes installs crawl.
const MinSiteBuildCPUMillis = 2000

func siteBuildLimits(memory, disk string, timeoutSeconds, concurrency int) SiteBuildTierLimits {
	return SiteBuildTierLimits{CPUMillis: MinSiteBuildCPUMillis, Memory: memory, Disk: disk,
		TimeoutSeconds: timeoutSeconds, Concurrency: concurrency}
}
