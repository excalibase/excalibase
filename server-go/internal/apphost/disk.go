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
	// Size is a whole number of mebibytes or gibibytes, such as "500Mi" or
	// "10Gi". On sized tenant storage it is a hard limit: a write past it fails.
	Size string `json:"size"`
	// Generation is the platform's, never the caller's: it counts the moves of
	// the disk onto a smaller volume and names the volume that holds it now.
	Generation int `json:"generation,omitempty"`
}

const (
	maxMountPathLength = 256
	mebibyte           = int64(1) << 20
	gibibyte           = int64(1) << 30
	// MinDiskBytes is the smallest disk: below it a filesystem is mostly its own overhead.
	MinDiskBytes = 64 * mebibyte
	// MaxDiskGeneration bounds how many times one disk may have been lowered.
	MaxDiskGeneration = 999999
)

var (
	// ErrInvalidDisk refuses a disk the renderer could not mount as data.
	ErrInvalidDisk = errors.New("invalid disk")
	// ErrDiskAbovePlan refuses a disk larger than the organisation's plan allows.
	ErrDiskAbovePlan = errors.New("the disk is larger than the plan allows")

	diskSize       = regexp.MustCompile(`^(?:([1-9][0-9]{0,6})Mi|([1-9][0-9]{0,4})Gi)$`)
	planDiskSize   = regexp.MustCompile(`^(?:(0|[1-9][0-9]{0,6})Mi|(0|[1-9][0-9]{0,5})Gi)$`)
	validMountPath = regexp.MustCompile(`^/[A-Za-z0-9._@+-][A-Za-z0-9._@+/-]*$`)

	// systemDirectories hold the image itself; a disk over one hides it.
	systemDirectories = map[string]bool{
		"/bin": true, "/boot": true, "/etc": true, "/lib": true, "/lib32": true, "/lib64": true,
		"/run": true, "/sbin": true, "/usr": true, "/var": true, "/var/run": true,
	}
	// kernelFilesystems are the runtime's, at any depth.
	kernelFilesystems = []string{"/proc", "/sys", "/dev"}
)

// Bytes is the disk's size in bytes.
func (d AppDisk) Bytes() (int64, error) {
	bytes, ok := sizeBytes(diskSize, d.Size)
	if !ok || bytes < MinDiskBytes {
		return 0, fmt.Errorf("%w: size must be a whole number of mebibytes from 64Mi or of gibibytes up to 99999Gi, such as 500Mi or 10Gi", ErrInvalidDisk)
	}
	return bytes, nil
}

// PlanDiskBytes reads a plan's app-disk cap; 0Gi (or 0Mi) means the plan offers none.
func PlanDiskBytes(size string) (int64, error) {
	bytes, ok := sizeBytes(planDiskSize, size)
	if !ok || (bytes > 0 && bytes < MinDiskBytes) {
		return 0, fmt.Errorf("the plan's app disk cap %q is not 0Gi or a whole number of mebibytes from 64Mi or of gibibytes", size)
	}
	return bytes, nil
}

func sizeBytes(pattern *regexp.Regexp, size string) (int64, bool) {
	match := pattern.FindStringSubmatch(size)
	if match == nil {
		return 0, false
	}
	if match[1] != "" {
		value, err := strconv.ParseInt(match[1], 10, 64)
		return value * mebibyte, err == nil
	}
	value, err := strconv.ParseInt(match[2], 10, 64)
	return value * gibibyte, err == nil
}

// FormatDiskSize writes bytes as whole gibibytes when they are, otherwise as
// mebibytes rounded up, so a usage is never shown smaller than it is.
func FormatDiskSize(bytes int64) string {
	if bytes > 0 && bytes%gibibyte == 0 {
		return strconv.FormatInt(bytes/gibibyte, 10) + "Gi"
	}
	return strconv.FormatInt((bytes+mebibyte-1)/mebibyte, 10) + "Mi"
}

// CheckDiskWithinPlan refuses a disk above the plan's cap in bytes.
func CheckDiskWithinPlan(disk *AppDisk, maxBytes int64) error {
	if disk == nil {
		return nil
	}
	size, err := disk.Bytes()
	if err != nil {
		return err
	}
	if maxBytes < MinDiskBytes {
		return fmt.Errorf("%w: the plan offers no app disks", ErrDiskAbovePlan)
	}
	if size > maxBytes {
		return fmt.Errorf("%w: the plan allows up to %s and the disk is %s", ErrDiskAbovePlan, FormatDiskSize(maxBytes), disk.Size)
	}
	return nil
}

func validateDisk(disk *AppDisk, replicas int) error {
	if disk == nil {
		return nil
	}
	if _, err := disk.Bytes(); err != nil {
		return err
	}
	if disk.Generation < 0 || disk.Generation > MaxDiskGeneration {
		return fmt.Errorf("%w: generation %d is out of range", ErrInvalidDisk, disk.Generation)
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

// DiskLimits answers the largest disk, in bytes, one app of a project may
// have: its organisation's current plan's cap.
type DiskLimits interface {
	MaxDiskBytes(ctx context.Context, projectID string) (int64, error)
}
