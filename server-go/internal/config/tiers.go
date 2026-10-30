package config

import (
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type TierConfig struct {
	// Version is the stored row's edit count, 0 for a built-in; a tier edit
	// sent from an older version is refused (EXC-620).
	Version     int
	MaxProjects int
	Instances   int
	StorageSize string
	// MaxStorageSize is the largest disk a project on the tier may grow to;
	// StorageSize is the disk it starts with (EXC-492).
	MaxStorageSize string
	// MaxAppDiskSize is the largest disk one app may have, in whole
	// gibibytes; "0Gi" offers none (EXC-523). An admin edits it with the plan.
	MaxAppDiskSize string
	// MaxApps is how many apps one project may hold; 0 offers none (EXC-524).
	// Existing apps above it after a downgrade are kept; only new ones are refused.
	MaxApps       int
	Memory        string
	CPU           string
	BackupEnabled bool
	// StatementTimeout caps how long a single query may run before Postgres
	// cancels it — the direct guard against a runaway query on a shared box.
	// Empty = no timeout (unbounded). Higher tiers get more headroom.
	StatementTimeout string
	// AutoPauseAfterDays pauses an ACTIVE project once nothing has touched it
	// for this many days (warning one day earlier). 0 = never auto-pause.
	AutoPauseAfterDays int
	// Sites is what one project's static sites may hold on the plan (EXC-620).
	Sites SiteTierLimits
	// SiteBuilds bound one git build of a site on the plan (EXC-354).
	SiteBuilds SiteBuildTierLimits
	// AppBuilds bound one git build of an app on the plan (EXC-547).
	AppBuilds AppBuildTierLimits
}

// SiteTierLimits bound a project's Sites. Files and file size start at
// Cloudflare's static-assets limits; the total keeps one upload off our disk.
// Sites above MaxSites after a downgrade are kept; only new ones are refused.
type SiteTierLimits struct {
	// MaxSites is how many sites one project may hold; 0 offers none.
	MaxSites       int
	MaxDeployBytes int64
	MaxFiles       int
	MaxFileBytes   int64
	// MaxDeploysPerHour is how many deploys one project may start in any hour.
	MaxDeploysPerHour int
	// MaxCustomDomains is how many custom domains one project's sites may hold (EXC-614);
	// Cloudflare includes 100 per zone and bills each one after.
	MaxCustomDomains int
}

// siteLimits holds every plan to Cloudflare's per-deploy limits; plans differ in sites and deploy rate.
func siteLimits(maxSites, deploysPerHour, customDomains int) SiteTierLimits {
	return SiteTierLimits{MaxSites: maxSites, MaxDeployBytes: 100 * mebibyte, MaxFiles: 20_000,
		MaxFileBytes: 25 * mebibyte, MaxDeploysPerHour: deploysPerHour, MaxCustomDomains: customDomains}
}

// CPUString returns CPU as configured (e.g. "0.5", "2"). Provided as a method
// so error messages don't leak the raw struct field name.
func (t TierConfig) CPUString() string { return t.CPU }

// MemoryString returns Memory as configured (e.g. "512Mi", "4Gi").
func (t TierConfig) MemoryString() string { return t.Memory }

// Paid tiers run a primary plus standbys, one per node (the cluster requires
// the anti-affinity, and admission refuses a platform with too few nodes).
// Counts are odd on purpose: engines that elect by quorum need it.
var tiers = map[domain.TierType]TierConfig{
	domain.Free: {
		MaxProjects:        1,
		Instances:          1,
		StorageSize:        "5Gi",
		MaxStorageSize:     "5Gi",
		MaxAppDiskSize:     "1Gi",
		MaxApps:            2,
		Memory:             "512Mi",
		CPU:                "0.5",
		BackupEnabled:      true,
		StatementTimeout:   "15s",
		AutoPauseAfterDays: 7,
		Sites:              siteLimits(5, 30, 2),
		SiteBuilds:         siteBuildLimits("2Gi", "5Gi", 10*60, 1),
		AppBuilds:          appBuildLimits(2000, "4Gi", "10Gi", 15*60, 1, "2Gi"),
	},
	domain.Standard: {
		MaxProjects:      5,
		Instances:        3,
		StorageSize:      "50Gi",
		MaxStorageSize:   "500Gi",
		MaxAppDiskSize:   "20Gi",
		MaxApps:          5,
		Memory:           "4Gi",
		CPU:              "2",
		BackupEnabled:    true,
		StatementTimeout: "30s",
		Sites:            siteLimits(20, 120, 5),
		SiteBuilds:       siteBuildLimits("4Gi", "10Gi", 20*60, 2),
		AppBuilds:        appBuildLimits(4000, "8Gi", "20Gi", 30*60, 2, "5Gi"),
	},
	domain.Enterprise: {
		MaxProjects:      0, // unlimited
		Instances:        5,
		StorageSize:      "500Gi",
		MaxStorageSize:   "2Ti",
		MaxAppDiskSize:   "100Gi",
		MaxApps:          20,
		Memory:           "16Gi",
		CPU:              "4",
		BackupEnabled:    true,
		StatementTimeout: "60s",
		Sites:            siteLimits(50, 300, 10),
		SiteBuilds:       siteBuildLimits("4Gi", "10Gi", 20*60, 2),
		AppBuilds:        appBuildLimits(4000, "8Gi", "20Gi", 30*60, 2, "5Gi"),
	},
}

func GetTierConfig(tier domain.TierType) (TierConfig, error) {
	tc, ok := tiers[tier]
	if !ok {
		return TierConfig{}, fmt.Errorf("unknown tier: %s", tier)
	}
	return tc, nil
}
