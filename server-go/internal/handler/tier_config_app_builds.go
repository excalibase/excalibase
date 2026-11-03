package handler

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// appBuildLimitsDTO is one app build's plan limits (EXC-547); each one left out keeps the plan's current value.
type appBuildLimitsDTO struct {
	AppBuildCPUMillis      *int    `json:"appBuildCpuMillis,omitempty"`
	AppBuildMemory         *string `json:"appBuildMemory,omitempty"`
	AppBuildDisk           *string `json:"appBuildDisk,omitempty"`
	AppBuildTimeoutSeconds *int    `json:"appBuildTimeoutSeconds,omitempty"`
	AppBuildConcurrency    *int    `json:"appBuildConcurrency,omitempty"`
	AppBuildMaxImage       *string `json:"appBuildMaxImage,omitempty"`
}

func (h *TierHandler) appBuildsOn(ctx context.Context) bool {
	return features.Enabled(ctx, h.flags, features.AppGitBuilds)
}

// shown drops the app build limits from a plan while appgitbuilds is off.
func (h *TierHandler) shown(ctx context.Context, dto tierConfigDTO) tierConfigDTO {
	if !h.appBuildsOn(ctx) {
		dto.appBuildLimitsDTO = appBuildLimitsDTO{}
	}
	return dto
}

func toAppBuildLimitsDTO(limits config.AppBuildTierLimits) appBuildLimitsDTO {
	return appBuildLimitsDTO{
		AppBuildCPUMillis: &limits.CPUMillis, AppBuildMemory: &limits.Memory, AppBuildDisk: &limits.Disk,
		AppBuildTimeoutSeconds: &limits.TimeoutSeconds, AppBuildConcurrency: &limits.Concurrency, AppBuildMaxImage: &limits.MaxImage,
	}
}

// overlay sets the limits the body names on the plan's current ones.
func (dto appBuildLimitsDTO) overlay(current config.AppBuildTierLimits) config.AppBuildTierLimits {
	out := current
	if dto.AppBuildCPUMillis != nil {
		out.CPUMillis = *dto.AppBuildCPUMillis
	}
	if dto.AppBuildMemory != nil {
		out.Memory = *dto.AppBuildMemory
	}
	if dto.AppBuildDisk != nil {
		out.Disk = *dto.AppBuildDisk
	}
	if dto.AppBuildTimeoutSeconds != nil {
		out.TimeoutSeconds = *dto.AppBuildTimeoutSeconds
	}
	if dto.AppBuildConcurrency != nil {
		out.Concurrency = *dto.AppBuildConcurrency
	}
	if dto.AppBuildMaxImage != nil {
		out.MaxImage = *dto.AppBuildMaxImage
	}
	return out
}

// Image and container builds need more room than a static site's: at least 1 GiB of memory and 2 GiB of disk.
var (
	minAppBuildMemory = resource.MustParse("1Gi")
	minAppBuildDisk   = resource.MustParse("2Gi")
	minAppBuildImage  = resource.MustParse("256Mi")
	maxAppBuildImage  = resource.MustParse("20Gi")
)

func validateAppBuildLimits(limits config.AppBuildTierLimits) error {
	if limits.CPUMillis < config.MinSiteBuildCPUMillis || limits.CPUMillis > maxSiteBuildCPUMillis {
		return fmt.Errorf("appBuildCpuMillis must be between %d and %d: gVisor bills its overhead to the build's own CPU",
			config.MinSiteBuildCPUMillis, maxSiteBuildCPUMillis)
	}
	if err := quantityBetween("appBuildMemory", limits.Memory, minAppBuildMemory, maxSiteBuildMemory); err != nil {
		return err
	}
	if err := quantityBetween("appBuildDisk", limits.Disk, minAppBuildDisk, maxSiteBuildDisk); err != nil {
		return err
	}
	if limits.TimeoutSeconds < minSiteBuildTimeout || limits.TimeoutSeconds > maxSiteBuildTimeout {
		return fmt.Errorf("appBuildTimeoutSeconds must be between %d and %d", minSiteBuildTimeout, maxSiteBuildTimeout)
	}
	if limits.Concurrency < 1 || limits.Concurrency > maxSiteBuildConcurrency {
		return fmt.Errorf("appBuildConcurrency must be between 1 and %d", maxSiteBuildConcurrency)
	}
	if err := quantityBetween("appBuildMaxImage", limits.MaxImage, minAppBuildImage, maxAppBuildImage); err != nil {
		return err
	}
	// The build packs the image next to itself on the work disk, so the disk holds it twice.
	image, disk := resource.MustParse(limits.MaxImage), resource.MustParse(limits.Disk)
	if 2*image.Value() > disk.Value() {
		return fmt.Errorf("appBuildDisk must be at least twice appBuildMaxImage: the image is packed on the build's own disk")
	}
	return nil
}
