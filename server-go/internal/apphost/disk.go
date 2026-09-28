package apphost

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// AppDisk is one persistent volume an app keeps across restarts, redeploys,
// pauses and renames (EXC-523). It is state, not config: it lives on the app
// record and is never frozen into a deploy's config, so a redeploy of an older
// config still mounts the app's current disk.
type AppDisk struct {
	// MountPath is where the container sees the disk.
	MountPath string `json:"mountPath"`
	// Size is a whole number of gibibytes, such as "10Gi". It only grows.
	Size string `json:"size"`
}

const maxMountPathLength = 256

var (
	// ErrInvalidDisk refuses a disk the renderer could not mount as data.
	ErrInvalidDisk = errors.New("invalid disk")
	// ErrDiskAbovePlan refuses a disk larger than the organisation's plan allows.
	ErrDiskAbovePlan = errors.New("the disk is larger than the plan allows")

	wholeDiskGiB     = regexp.MustCompile(`^[1-9][0-9]{0,4}Gi$`)
	wholePlanDiskGiB = regexp.MustCompile(`^(0|[1-9][0-9]{0,5})Gi$`)
	validMountPath   = regexp.MustCompile(`^/[A-Za-z0-9._@+-][A-Za-z0-9._@+/-]*$`)

	// systemDirectories hold the image itself; a disk over one hides it.
	systemDirectories = map[string]bool{
		"/bin": true, "/boot": true, "/etc": true, "/lib": true, "/lib32": true, "/lib64": true,
		"/run": true, "/sbin": true, "/usr": true, "/var": true, "/var/run": true,
	}
	// kernelFilesystems are the runtime's, at any depth.
	kernelFilesystems = []string{"/proc", "/sys", "/dev"}
)

// GiB is the disk's size in gibibytes.
func (d AppDisk) GiB() (int, error) {
	if !wholeDiskGiB.MatchString(d.Size) {
		return 0, fmt.Errorf("%w: size must be a whole number of gibibytes from 1Gi to 99999Gi, such as 10Gi", ErrInvalidDisk)
	}
	return strconv.Atoi(strings.TrimSuffix(d.Size, "Gi"))
}

// PlanDiskGiB reads a plan's app-disk cap; 0Gi means the plan offers none.
func PlanDiskGiB(size string) (int, error) {
	if !wholePlanDiskGiB.MatchString(size) {
		return 0, fmt.Errorf("the plan's app disk cap %q is not a whole number of gibibytes", size)
	}
	return strconv.Atoi(strings.TrimSuffix(size, "Gi"))
}

// CheckDiskWithinPlan refuses a disk above the plan's cap in gibibytes.
func CheckDiskWithinPlan(disk *AppDisk, maxGiB int) error {
	if disk == nil {
		return nil
	}
	size, err := disk.GiB()
	if err != nil {
		return err
	}
	if size > maxGiB {
		return fmt.Errorf("%w: the plan allows up to %dGi and the disk is %s", ErrDiskAbovePlan, maxGiB, disk.Size)
	}
	return nil
}

func validateDisk(disk *AppDisk, replicas int) error {
	if disk == nil {
		return nil
	}
	if _, err := disk.GiB(); err != nil {
		return err
	}
	if err := validateMountPath(disk.MountPath); err != nil {
		return err
	}
	if replicas > 1 {
		return fmt.Errorf("%w: an app with a disk runs one copy; set replicas to 0 or 1", ErrInvalidDisk)
	}
	return nil
}

func validateMountPath(mountPath string) error {
	if len(mountPath) > maxMountPathLength || !validMountPath.MatchString(mountPath) || path.Clean(mountPath) != mountPath {
		return fmt.Errorf("%w: the mount path must be a clean absolute path of letters, digits and . _ - @ +", ErrInvalidDisk)
	}
	if systemDirectories[mountPath] {
		return fmt.Errorf("%w: %s holds the image's own files; mount the disk at a data directory such as /data", ErrInvalidDisk, mountPath)
	}
	for _, kernel := range kernelFilesystems {
		if mountPath == kernel || strings.HasPrefix(mountPath, kernel+"/") {
			return fmt.Errorf("%w: %s belongs to the container runtime", ErrInvalidDisk, mountPath)
		}
	}
	return nil
}

// DiskLimits answers the largest disk, in gibibytes, one app of a project may
// have: its organisation's current plan's cap.
type DiskLimits interface {
	MaxDiskGiB(ctx context.Context, projectID string) (int, error)
}
