package dockerapps

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// APP_USERNS (EXC-682): how unsandboxed apps on rootless Podman are kept from
// each other's files. Shared is one user range for every app: an app that
// escapes its container owns what every other app's volume holds. Per-app
// gives each app a fixed range of the platform user's subuid/subgid pool.
const (
	UsernsAuto   = "auto"
	UsernsPerApp = "per-app"
	UsernsShared = "shared"
)

const (
	// defaultUsernsRangeSize is a full container user space: ids 0 to 65535.
	defaultUsernsRangeSize = 65536
	// defaultUsernsFirstBase starts per-app ranges after the 65536 ids the platform user's
	// own namespace gives every shared container (ids 1 to 65536 of it).
	defaultUsernsFirstBase = 65537
	// growSubidsHint is what an operator runs, as root, to make room.
	growSubidsHint = "run `deploy/single-host/init.sh --grow-subids <platform user> --max-apps <n>` as root, " +
		"then `podman system migrate` as that user and restart the platform"
	poolMarker = "---"
)

// toolReadPool prints the platform user's namespace as a container sees it: the ids its apps' ranges come from.

var toolReadPool = []string{"sh", "-c", "cat /proc/self/uid_map; echo " + poolMarker + "; cat /proc/self/gid_map"}

// ErrUsernsPoolFull: every per-app range the pool holds is taken.
var ErrUsernsPoolFull = errors.New("no per-app user range is free")

// usernsRange is the ids of the platform user's namespace one app maps to its 0..Size-1.
type usernsRange struct {
	Base uint32 `json:"base"`
	Size uint32 `json:"size"`
}

func (u usernsRange) end() uint64 { return uint64(u.Base) + uint64(u.Size) }

func (u usernsRange) overlaps(other usernsRange) bool {
	return uint64(u.Base) < other.end() && uint64(other.Base) < u.end()
}

// mode is Podman's --userns for it: root of the container is Base, the same for groups.
func (u usernsRange) mode() container.UsernsMode {
	mapping := fmt.Sprintf("0:%d:%d", u.Base, u.Size)
	return container.UsernsMode("auto:uidmapping=" + mapping + ",gidmapping=" + mapping)
}

// label is the range as engine objects carry it, so a range stays taken while any of them lives.
func (u usernsRange) label() string { return fmt.Sprintf("%d:%d", u.Base, u.Size) }

func parseUsernsLabel(value string) (usernsRange, bool) {
	base, size, ok := strings.Cut(value, ":")
	if !ok {
		return usernsRange{}, false
	}
	b, errBase := strconv.ParseUint(base, 10, 32)
	s, errSize := strconv.ParseUint(size, 10, 32)
	if errBase != nil || errSize != nil || b == 0 || s == 0 || b+s > 1<<32 {
		return usernsRange{}, false
	}
	return usernsRange{Base: uint32(b), Size: uint32(s)}, true
}

// appUserns is the range kept on the app's record for good.
type appUserns struct {
	Mode  string       `json:"mode"`
	Range *usernsRange `json:"range,omitempty"`
}

// idPool is how far the platform user's namespace is mapped, from id 0 without a gap, for users and groups.
type idPool struct{ uidEnd, gidEnd uint64 }

func (p idPool) end() uint64 { return min(p.uidEnd, p.gidEnd) }

// parseIDMaps reads the output of toolReadPool: the uid map, a marker, the gid map.
func parseIDMaps(output string) (idPool, error) {
	uids, gids, found := strings.Cut(output, poolMarker)
	if !found {
		return idPool{}, errors.New("the engine printed no group map")
	}
	uidEnd, err := contiguousEnd(uids)
	if err != nil {
		return idPool{}, fmt.Errorf("user map: %w", err)
	}
	gidEnd, err := contiguousEnd(gids)
	if err != nil {
		return idPool{}, fmt.Errorf("group map: %w", err)
	}
	return idPool{uidEnd: uidEnd, gidEnd: gidEnd}, nil
}

// contiguousEnd walks "inside outside count" lines and answers the first id
// not mapped, counting from 0: a range spanning a gap could not be mapped.
func contiguousEnd(table string) (uint64, error) {
	end := uint64(0)
	lines := 0
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return 0, fmt.Errorf("%q is not a map line", line)
		}
		inside, errInside := strconv.ParseUint(fields[0], 10, 64)
		count, errCount := strconv.ParseUint(fields[2], 10, 64)
		if errInside != nil || errCount != nil {
			return 0, fmt.Errorf("%q is not a map line", line)
		}
		lines++
		if inside == end {
			end += count
		}
	}
	if lines == 0 {
		return 0, errors.New("empty map")
	}
	return end, nil
}

// hostKind is what decides whether per-app ranges apply at all.
type hostKind struct{ rootlessPodman, sandboxed bool }

// usernsPlan is what startup decided for new apps.
type usernsPlan struct {
	perApp bool
	// slots is how many ranges the pool holds; poolEnd how far it is mapped (0: not read).
	slots   int
	poolEnd uint64
	// notice is the one warning for the operator, empty when there is nothing to say.
	notice string
}

func planUserns(setting string, host hostKind, pool idPool, first, size uint32) (usernsPlan, error) {
	switch setting {
	case "", UsernsShared:
		return usernsPlan{}, nil
	case UsernsAuto, UsernsPerApp:
	default:
		return usernsPlan{}, fmt.Errorf("APP_USERNS %q must be auto, per-app or shared", setting)
	}
	strict := setting == UsernsPerApp
	switch {
	case !host.rootlessPodman:
		if strict {
			return usernsPlan{}, errors.New("APP_USERNS=per-app is for apps on rootless Podman; Docker and rootful engines are not changed by it")
		}
		return usernsPlan{}, nil
	case host.sandboxed:
		if strict {
			return usernsPlan{}, errors.New("APP_USERNS=per-app is for apps run without a sandbox (APP_SANDBOX_RUNTIME=none); gVisor already separates them")
		}
		return usernsPlan{}, nil
	}
	plan := usernsPlan{poolEnd: pool.end()}
	if plan.poolEnd > uint64(first) {
		plan.slots = int((plan.poolEnd - uint64(first)) / uint64(size))
	}
	if plan.slots == 0 {
		have := plan.poolEnd
		if have > 0 {
			have--
		}
		if strict {
			return usernsPlan{}, fmt.Errorf("APP_USERNS=per-app: the platform user's /etc/subuid and /etc/subgid map %d ids, with no room beyond the %d the shared range uses for even one app of %d; %s",
				have, first-1, size, growSubidsHint)
		}
		plan.notice = fmt.Sprintf("APP_USERNS=auto: apps share one user range, because the platform user's /etc/subuid and /etc/subgid map %d ids, "+
			"too few for a range of %d per app; to give each app its own, %s", have, size, growSubidsHint)
		return plan, nil
	}
	plan.perApp = true
	return plan, nil
}

// freeRange is the lowest range of the pool that touches none of used.
func freeRange(used []usernsRange, first, size uint32, slots int) (usernsRange, bool) {
	for slot := range slots {
		candidate := usernsRange{Base: first + uint32(slot)*size, Size: size}
		busy := false
		for _, taken := range used {
			if candidate.overlaps(taken) {
				busy = true
				break
			}
		}
		if !busy {
			return candidate, true
		}
	}
	return usernsRange{}, false
}

func (r *Runtime) usernsRangeSize() uint32 {
	if r.opts.UsernsRangeSize > 0 {
		return r.opts.UsernsRangeSize
	}
	return defaultUsernsRangeSize
}

func (r *Runtime) usernsFirstBase() uint32 {
	if r.opts.UsernsFirstBase > 0 {
		return r.opts.UsernsFirstBase
	}
	return defaultUsernsFirstBase
}

// verifyUserns reads the pool the platform user owns and decides, once at startup.
func (r *Runtime) verifyUserns(ctx context.Context) error {
	setting := r.opts.Userns
	if setting == "" || setting == UsernsShared {
		return nil
	}
	host, err := r.hostKind(ctx)
	if err != nil {
		return err
	}
	var pool idPool
	if host.rootlessPodman && !host.sandboxed {
		var err error
		if pool, err = r.readPool(ctx); err != nil {
			if setting == UsernsPerApp {
				return err
			}
			// Auto never keeps the platform from starting: without a pool it is shared.
			r.usernsMu.Lock()
			r.userns = usernsPlan{notice: "APP_USERNS=auto: apps share one user range, because " + err.Error()}
			r.usernsMu.Unlock()
			return nil
		}
	}
	plan, err := planUserns(setting, host, pool, r.usernsFirstBase(), r.usernsRangeSize())
	if err != nil {
		return err
	}
	r.usernsMu.Lock()
	r.userns = plan
	r.usernsMu.Unlock()
	return nil
}

func (r *Runtime) readPool(ctx context.Context) (idPool, error) {
	output, err := r.runTool(ctx, k8s.DiskJobOptions{Timeout: toolCheckTimeout}, nil, nil, toolReadPool)
	if err != nil {
		return idPool{}, fmt.Errorf("the platform user's id map could not be read: %w", err)
	}
	pool, err := parseIDMaps(output)
	if err != nil {
		return idPool{}, fmt.Errorf("the platform user's id map could not be read: %w", err)
	}
	return pool, nil
}

func (r *Runtime) hostKind(ctx context.Context) (hostKind, error) {
	info, err := r.engine.Info(ctx)
	if err != nil {
		return hostKind{}, fmt.Errorf("read the engine's settings: %w", err)
	}
	podman, err := IsPodman(ctx, r.engine)
	if err != nil {
		return hostKind{}, err
	}
	rootless := false
	for _, option := range info.SecurityOptions {
		rootless = rootless || option == "name=rootless"
	}
	return hostKind{rootlessPodman: podman && rootless, sandboxed: r.opts.SandboxRuntime != ""}, nil
}

// UsernsNotice is the one warning startup has for the operator about APP_USERNS, if any.
func (r *Runtime) UsernsNotice() string {
	r.usernsMu.Lock()
	defer r.usernsMu.Unlock()
	return r.userns.notice
}

// appUserns answers the range an app runs in, nil when it shares one. The
// first call for an app decides and writes the decision to its record; every
// later one, after any redeploy, restart or upgrade, answers the same.
func (r *Runtime) appUserns(ctx context.Context, project, appID string) (*usernsRange, error) {
	r.usernsMu.Lock()
	defer r.usernsMu.Unlock()
	record, err := r.loadRecord(project, appID)
	if err != nil {
		return nil, err
	}
	if record != nil && record.Userns != nil {
		return r.assignedRange(record.Userns, appID)
	}
	decision, err := r.decideUserns(ctx, project, appID, record)
	if err != nil || decision == nil {
		return nil, err
	}
	// Only a range is written down: an app that is shared has no record of it, and is found
	// shared again by what it already runs, so a host that never uses ranges changes nothing.
	if err := r.updateRecord(project, appID, func(record *appRecord) error {
		record.Userns = decision
		return nil
	}); err != nil {
		return nil, err
	}
	return decision.Range, nil
}

// assignedRange refuses to run an app in a range this host can no longer give it.
func (r *Runtime) assignedRange(decision *appUserns, appID string) (*usernsRange, error) {
	if decision.Range == nil {
		return nil, nil
	}
	if r.opts.SandboxRuntime != "" {
		return nil, fmt.Errorf("app %s owns its files through a per-app user range, which a sandboxed runtime cannot give it", appID)
	}
	if r.userns.poolEnd != 0 && decision.Range.end() > r.userns.poolEnd {
		return nil, fmt.Errorf("app %s runs in ids up to %d, past the %d the platform user's /etc/subuid maps now; %s",
			appID, decision.Range.end(), r.userns.poolEnd, growSubidsHint)
	}
	return decision.Range, nil
}

// recordedUserns is the range an app already has, for work on its existing disk; it decides nothing.
func (r *Runtime) recordedUserns(project, appID string) (*usernsRange, error) {
	record, err := r.loadRecord(project, appID)
	if err != nil || record == nil || record.Userns == nil {
		return nil, err
	}
	return record.Userns.Range, nil
}

// decideUserns answers the range a new app gets, nil when it shares the common one.
func (r *Runtime) decideUserns(ctx context.Context, project, appID string, record *appRecord) (*appUserns, error) {
	if !r.userns.perApp {
		return nil, nil
	}
	// What an earlier release made is owned by the shared user: chown it into a range
	// only at an operator's explicit step, never because a redeploy happened to come.
	legacy, err := r.hasUnrangedFootprint(ctx, appID, record)
	if err != nil {
		return nil, err
	}
	if legacy {
		return nil, nil
	}
	used, err := r.usedRanges(ctx)
	if err != nil {
		return nil, err
	}
	free, ok := freeRange(used, r.usernsFirstBase(), r.usernsRangeSize(), r.userns.slots)
	if ok {
		return &appUserns{Mode: UsernsPerApp, Range: &free}, nil
	}
	full := fmt.Errorf("%w: the platform user's /etc/subuid holds %d ranges and all are in use; %s", ErrUsernsPoolFull, r.userns.slots, growSubidsHint)
	if r.opts.Userns == UsernsPerApp {
		return nil, full
	}
	log.Printf("WARN: APP_USERNS=auto: app %s of project %s shares the common user range: %v", appID, project, full)
	return nil, nil
}

// hasUnrangedFootprint: the app already has a deploy, a container or a disk
// that no range was given to.
func (r *Runtime) hasUnrangedFootprint(ctx context.Context, appID string, record *appRecord) (bool, error) {
	if record != nil && (record.Deploy != "" || record.Pending != nil) {
		return true, nil
	}
	containers, err := r.listApps(ctx, labelApp, appID)
	if err != nil {
		return false, err
	}
	for _, summary := range containers {
		if summary.Labels[labelUserns] == "" {
			return true, nil
		}
	}
	volumes, err := r.engine.VolumeList(ctx, volume.ListOptions{Filters: labelFilter(labelApp, appID)})
	if err != nil {
		return false, fmt.Errorf("list app disks: %w", err)
	}
	for _, disk := range volumes.Volumes {
		if disk.Labels[labelUserns] == "" {
			return true, nil
		}
	}
	return false, nil
}

// usedRanges is every range an app record, an app container or a disk holds: a range is
// free only when none does, so a disk that outlives its app keeps its owner's range.
func (r *Runtime) usedRanges(ctx context.Context) ([]usernsRange, error) {
	var used []usernsRange
	records, err := r.projectRecords("")
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.Userns != nil && record.Userns.Range != nil {
			used = append(used, *record.Userns.Range)
		}
	}
	containers, err := r.listApps(ctx)
	if err != nil {
		return nil, err
	}
	labels := make([]map[string]string, 0, len(containers))
	for _, summary := range containers {
		labels = append(labels, summary.Labels)
	}
	volumes, err := r.engine.VolumeList(ctx, volume.ListOptions{Filters: labelFilter()})
	if err != nil {
		return nil, fmt.Errorf("list app disks: %w", err)
	}
	for _, disk := range volumes.Volumes {
		labels = append(labels, disk.Labels)
	}
	for _, held := range labels {
		if taken, ok := parseUsernsLabel(held[labelUserns]); ok {
			used = append(used, taken)
		}
	}
	return used, nil
}

// usernsOf reads the range a container was made in; nil for one in the shared range.
func usernsOf(labels map[string]string) *usernsRange {
	if parsed, ok := parseUsernsLabel(labels[labelUserns]); ok {
		return &parsed
	}
	return nil
}

// withUsernsLabel puts the app's range on an engine object's labels.
func withUsernsLabel(labels map[string]string, userns *usernsRange) map[string]string {
	if userns != nil {
		labels[labelUserns] = userns.label()
	}
	return labels
}
