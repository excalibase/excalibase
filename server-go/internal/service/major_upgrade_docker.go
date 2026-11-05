package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// DockerMajorUpgrader moves a single host's Postgres to a newer major. The
// official image has no pg_upgrade for an older major, so the data moves by
// dump and load: the old server is fenced off the network, its last WAL
// shipped, dumped whole, stopped and renamed; a new container of the new
// image, under the old name and host port, loads the dump on its first
// start, before its server is on the network. Until the platform records the
// new container, the old one is put back as it was; once it has, recovery
// records a fully loaded conversion instead of putting it back.
type DockerMajorUpgrader struct {
	adapter *DockerBackupAdapter
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
	timings DockerUpgradeTimings
}

// DockerUpgradeTimings bounds each wait; zero values take the defaults.
type DockerUpgradeTimings struct {
	Poll  time.Duration // between looks at a container
	Load  time.Duration // the new container loads the dump within this
	Start time.Duration // a server starts within this
}

type DockerMajorUpgraderConfig struct {
	Backups *DockerBackupAdapter
	Timings DockerUpgradeTimings
	Now     func() time.Time
	Sleep   func(context.Context, time.Duration) error
}

func NewDockerMajorUpgrader(cfg DockerMajorUpgraderConfig) *DockerMajorUpgrader {
	u := &DockerMajorUpgrader{adapter: cfg.Backups, now: cfg.Now, sleep: cfg.Sleep, timings: cfg.Timings}
	if u.now == nil {
		u.now = time.Now
	}
	if u.sleep == nil {
		u.sleep = sleepCtx
	}
	if u.timings.Poll <= 0 {
		u.timings.Poll = 2 * time.Second
	}
	if u.timings.Load <= 0 {
		u.timings.Load = 4 * time.Hour
	}
	if u.timings.Start <= 0 {
		u.timings.Start = 2 * time.Minute
	}
	return u
}

const noRenameBlocker = "this host's container engine cannot rename containers, which the upgrade needs"

var errDockerNotConfigured = errors.New("docker client not configured")

func (u *DockerMajorUpgrader) engine() (provisioner.DockerClient, provisioner.ContainerRenamer, error) {
	u.adapter.mu.RLock()
	dc := u.adapter.docker
	u.adapter.mu.RUnlock()
	if dc == nil {
		return nil, nil, errDockerNotConfigured
	}
	renamer, _ := dc.(provisioner.ContainerRenamer)
	return dc, renamer, nil
}

func runScript(ctx context.Context, dc provisioner.DockerClient, container, script string) (string, error) {
	return dc.ExecInContainerStdin(ctx, container, []string{"sh", "-s"}, script)
}

// Preflight runs the database checks in the project's container. See
// MajorUpgrader.
func (u *DockerMajorUpgrader) Preflight(ctx context.Context, inst *domain.DatabaseInstance, target string) ([]string, error) {
	dc, renamer, err := u.engine()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMajorUpgradeCheckUnavailable, err)
	}
	if renamer == nil {
		return []string{noRenameBlocker}, nil
	}
	findings, err := u.findings(ctx, dc, inst)
	if err != nil {
		return nil, err
	}
	return preflightBlockers(findings, target, nil), nil
}

func (u *DockerMajorUpgrader) findings(ctx context.Context, dc provisioner.DockerClient, inst *domain.DatabaseInstance) (preflightFindings, error) {
	out, err := runScript(ctx, dc, inst.Namespace, preflightScript())
	if err != nil {
		return preflightFindings{}, fmt.Errorf("%w: %v", ErrMajorUpgradeCheckUnavailable, err)
	}
	findings, err := parsePreflight(out)
	if err != nil {
		return preflightFindings{}, fmt.Errorf("%w: %v", ErrMajorUpgradeCheckUnavailable, err)
	}
	return findings, nil
}

// TakeBackup takes a base backup and returns once it is stored. See
// MajorUpgrader.
func (u *DockerMajorUpgrader) TakeBackup(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	return u.adapter.TakeSafetyBackup(ctx, inst)
}

// swap is what a conversion has done so far, for putting it back.
type swap struct {
	inst     *domain.DatabaseInstance
	run      domain.MajorUpgrade
	oldID    string
	renamed  bool
	newID    string
	recorded bool
}

// Convert moves the data into a container of the new major. See
// MajorUpgrader.
func (u *DockerMajorUpgrader) Convert(ctx context.Context, inst *domain.DatabaseInstance, run domain.MajorUpgrade, progress UpgradeProgress) UpgradeOutcome {
	if err := progress.Step(ctx, domain.MajorUpgradeStepConverting); err != nil {
		return stopped(run, err)
	}
	dc, renamer, err := u.engine()
	if err != nil {
		return nothingChanged(run, err)
	}
	if renamer == nil {
		return nothingChanged(run, errors.New(noRenameBlocker))
	}
	image, err := config.DockerPostgresImage(run.ToVersion)
	if err != nil {
		return nothingChanged(run, err)
	}
	if outcome, ok := u.checkExtensions(ctx, dc, inst, run, image); !ok {
		return outcome
	}
	spec, err := u.newContainerSpec(ctx, inst, image)
	if err != nil {
		return nothingChanged(run, err)
	}
	state := &swap{inst: inst, run: run, oldID: inst.Namespace}
	if err := u.moveData(ctx, dc, renamer, spec, state, progress); err != nil {
		return u.undo(ctx, dc, renamer, state, progress, err)
	}
	if err := progress.Hold(ctx); err != nil {
		return u.undo(ctx, dc, renamer, state, progress, err)
	}
	if err := u.adapter.recordContainer(inst, state.newID); err != nil {
		return u.undo(ctx, dc, renamer, state, progress, err)
	}
	state.recorded = true
	if err := progress.Converted(ctx, u.now()); err != nil {
		return notRecorded(err)
	}
	return UpgradeOutcome{Converted: true}
}

// checkExtensions refuses, before anything changes, a target image that
// lacks an extension a database uses. The image is read from a container
// that is created, never started.
func (u *DockerMajorUpgrader) checkExtensions(ctx context.Context, dc provisioner.DockerClient, inst *domain.DatabaseInstance, run domain.MajorUpgrade, image string) (UpgradeOutcome, bool) {
	findings, err := u.findings(ctx, dc, inst)
	if err != nil {
		return nothingChanged(run, err), false
	}
	available, err := u.imageExtensions(ctx, dc, inst, run, image)
	if err != nil {
		return nothingChanged(run, err), false
	}
	if blockers := preflightBlockers(findings, run.ToVersion, available); len(blockers) > 0 {
		outcome := nothingChanged(run, errors.New(strings.Join(blockers, "; ")))
		outcome.Public = strings.Join(blockers, "; ") + "; nothing was changed"
		return outcome, false
	}
	return UpgradeOutcome{}, true
}

func (u *DockerMajorUpgrader) imageExtensions(ctx context.Context, dc provisioner.DockerClient, inst *domain.DatabaseInstance, run domain.MajorUpgrade, image string) (map[string]bool, error) {
	scratch, err := dc.CreateContainerSpec(ctx, provisioner.ContainerSpec{
		Name: provisioner.DatabaseContainerName(inst.ProjectID) + "-extensions-" + run.ID, Image: image,
	})
	if err != nil {
		return nil, fmt.Errorf("read the extensions of %s: %w", image, err)
	}
	defer func() {
		if err := dc.RemoveContainer(context.WithoutCancel(ctx), scratch); err != nil {
			log.Printf("major upgrade %s: remove %s: %v", inst.ProjectID, scratch, err)
		}
	}()
	listing, err := dc.CopyFromContainer(ctx, scratch, provisioner.ExtensionDir(run.ToVersion))
	if err != nil {
		return nil, fmt.Errorf("read the extensions of %s: %w", image, err)
	}
	defer listing.Close()
	names, err := provisioner.ExtensionNames(listing)
	if err != nil {
		return nil, err
	}
	available := make(map[string]bool, len(names))
	for _, name := range names {
		available[name] = true
	}
	return available, nil
}

func (u *DockerMajorUpgrader) newContainerSpec(ctx context.Context, inst *domain.DatabaseInstance, image string) (provisioner.ContainerSpec, error) {
	u.adapter.mu.RLock()
	plans := u.adapter.plans
	u.adapter.mu.RUnlock()
	plan, limits, err := restorePlanOf(ctx, plans, inst)
	if err != nil {
		return provisioner.ContainerSpec{}, err
	}
	ports, err := u.adapter.heldPorts(ctx, inst.ProjectID, false)
	if err != nil {
		return provisioner.ContainerSpec{}, err
	}
	return provisioner.ContainerSpec{
		Name:  provisioner.DatabaseContainerName(inst.ProjectID),
		Image: image,
		// The dump creates every other database, with its owner and settings.
		Env: map[string]string{
			"POSTGRES_DB": "postgres", "POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": provisioner.NewOwnerPassword(),
		},
		Ports:  map[string]string{"5432": ports.Postgres},
		Limits: limits,
		Cmd:    provisioner.PostgresCommand(plan.Config),
	}, nil
}

// moveData fences, ships, dumps and swaps the containers, and waits for the
// new one to load the dump.
func (u *DockerMajorUpgrader) moveData(ctx context.Context, dc provisioner.DockerClient, renamer provisioner.ContainerRenamer,
	spec provisioner.ContainerSpec, state *swap, progress UpgradeProgress) error {
	if _, err := runScript(ctx, dc, state.oldID, provisioner.FenceScript()); err != nil {
		return fmt.Errorf("fence: %w", err)
	}
	if err := progress.WritesStopped(ctx, u.now()); err != nil {
		return err
	}
	if err := u.adapter.RefreshWALArchive(ctx, state.inst); err != nil {
		return fmt.Errorf("ship the last WAL: %w", err)
	}
	if _, err := runScript(ctx, dc, state.oldID, provisioner.DumpScript()); err != nil {
		return fmt.Errorf("dump: %w", err)
	}
	if err := progress.Hold(ctx); err != nil {
		return err
	}
	if err := dc.StopContainer(ctx, state.oldID); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if err := progress.Hold(ctx); err != nil {
		return err
	}
	if err := renamer.RenameContainer(ctx, state.oldID, provisioner.PreUpgradeContainerName(state.inst.ProjectID, state.run.ID)); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	state.renamed = true
	newID, err := dc.CreateContainerSpec(ctx, spec)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	state.newID = newID
	if err := u.seed(ctx, dc, state); err != nil {
		return err
	}
	if err := provisioner.StartHoldingPort(ctx, dc, restoreStartPoller, newID); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return u.waitLoaded(ctx, dc, newID)
}

// seed copies the archive directory, the dump and the first-start script
// into the new container.
func (u *DockerMajorUpgrader) seed(ctx context.Context, dc provisioner.DockerClient, state *swap) error {
	archive, err := provisioner.WALArchiveDirTar(provisioner.PostgresImageOwner)
	if err != nil {
		return err
	}
	if err := dc.CopyToContainer(ctx, state.newID, "/", bytes.NewReader(archive)); err != nil {
		return fmt.Errorf("create the WAL archive: %w", err)
	}
	dump, err := dc.CopyFromContainer(ctx, state.oldID, provisioner.UpgradeDir)
	if err != nil {
		return fmt.Errorf("read the dump: %w", err)
	}
	err = dc.CopyToContainer(ctx, state.newID, "/tmp", dump)
	dump.Close()
	if err != nil {
		return fmt.Errorf("copy the dump: %w", err)
	}
	script, err := provisioner.UpgradeInitTar(state.inst.DatabaseName)
	if err != nil {
		return err
	}
	if err := dc.CopyToContainer(ctx, state.newID, provisioner.UpgradeInitDir, bytes.NewReader(script)); err != nil {
		return fmt.Errorf("copy the load script: %w", err)
	}
	return nil
}

var errLoadFailed = errors.New("the new version could not load the database")

// loadFailure is errLoadFailed with the load's last errors, for the log only.
func (u *DockerMajorUpgrader) loadFailure(ctx context.Context, dc provisioner.DockerClient, container string) error {
	out, err := runScript(ctx, dc, container, provisioner.LoadErrorsScript())
	if err != nil || strings.TrimSpace(out) == "" {
		return errLoadFailed
	}
	return fmt.Errorf("%w: %s", errLoadFailed, strings.TrimSpace(out))
}

// waitLoaded waits for the first start to say how the load ended and the
// real server to listen.
func (u *DockerMajorUpgrader) waitLoaded(ctx context.Context, dc provisioner.DockerClient, container string) error {
	deadline := u.now().Add(u.timings.Load)
	for u.now().Before(deadline) {
		out, err := runScript(ctx, dc, container, provisioner.UpgradeStateScript())
		switch strings.TrimSpace(out) {
		case provisioner.UpgradeFailed:
			return u.loadFailure(ctx, dc, container)
		case provisioner.UpgradeLoaded:
			if code, err := dc.ExecInContainer(ctx, container, tcpReady); err == nil && code == 0 {
				return nil
			}
		default:
			if err != nil {
				log.Printf("major upgrade: read load state of %s: %v", container, err)
			}
		}
		if err := u.sleep(ctx, u.timings.Poll); err != nil {
			return err
		}
	}
	return fmt.Errorf("the load did not end within %s", u.timings.Load)
}

// tcpReady is true once the real server listens; the first start's
// temporary server takes socket connections only.
var tcpReady = []string{"pg_isready", "-h", "127.0.0.1", "-U", "postgres"}

const (
	putBackPublic = "the upgrade did not complete, so the database was put back on Postgres %s"
)

// undo puts the old container back, unless the run was lost: then what is
// on the host is left for the actor that owns the run now.
func (u *DockerMajorUpgrader) undo(ctx context.Context, dc provisioner.DockerClient, renamer provisioner.ContainerRenamer,
	state *swap, progress UpgradeProgress, cause error) UpgradeOutcome {
	if lost := lostRun(ctx, cause, progress); lost != nil {
		return UpgradeOutcome{Cause: lost}
	}
	return u.putBack(ctx, dc, renamer, state, cause)
}

// putBack returns the old container to its name, the project and service.
// No call is cut off mid-flight, but once the run is lost no further step
// starts: what is left is for the actor that owns the run now.
func (u *DockerMajorUpgrader) putBack(ctx context.Context, dc provisioner.DockerClient, renamer provisioner.ContainerRenamer, state *swap, cause error) UpgradeOutcome {
	ctx = detachFence(ctx)
	log.Printf("major upgrade %s: putting Postgres %s back: %v", state.inst.ProjectID, state.run.FromVersion, cause)
	var problems []error
	for _, step := range u.putBackSteps(dc, renamer, state) {
		if lost := runLostBy(ctx); lost != nil {
			return gaveUpPutBack(lost, cause)
		}
		problems = append(problems, step(ctx))
	}
	err := errors.Join(problems...)
	if err == nil {
		err = u.serveOld(ctx, dc, state.oldID)
	}
	if lost := runLostBy(ctx); lost != nil {
		return gaveUpPutBack(lost, errors.Join(cause, err))
	}
	if err != nil {
		u.dropDump(ctx, dc, state.oldID)
		return UpgradeOutcome{Public: notPutBackPublic, Cause: errors.Join(cause, err)}
	}
	return UpgradeOutcome{RolledBack: true, Cause: cause, Public: fmt.Sprintf(putBackPublic, state.run.FromVersion)}
}

// putBackSteps undo the swap as far as it went.
func (u *DockerMajorUpgrader) putBackSteps(dc provisioner.DockerClient, renamer provisioner.ContainerRenamer, state *swap) []func(context.Context) error {
	var steps []func(context.Context) error
	if state.newID != "" {
		steps = append(steps, func(ctx context.Context) error { return dc.RemoveContainer(ctx, state.newID) })
	}
	if state.renamed {
		steps = append(steps, func(ctx context.Context) error {
			return renamer.RenameContainer(ctx, state.oldID, provisioner.DatabaseContainerName(state.inst.ProjectID))
		})
	}
	if state.recorded {
		steps = append(steps, func(context.Context) error { return u.adapter.recordContainer(state.inst, state.oldID) })
	}
	return steps
}

func gaveUpPutBack(lost, cause error) UpgradeOutcome {
	return UpgradeOutcome{Cause: fmt.Errorf("%w (putting back: %v)", lost, cause)}
}

// dropDump removes the dump, which holds every password hash, from a
// container left as it is; a stopped one cannot be reached and keeps it until
// it is started again or removed.
func (u *DockerMajorUpgrader) dropDump(ctx context.Context, dc provisioner.DockerClient, container string) {
	if _, err := runScript(ctx, dc, container, provisioner.CleanupScript()); err != nil {
		log.Printf("major upgrade: remove the dump from %s: %v", container, err)
	}
}

// serveOld starts the old container when it is stopped, waits for its
// server, lifts the fence and removes the dump.
func (u *DockerMajorUpgrader) serveOld(ctx context.Context, dc provisioner.DockerClient, container string) error {
	current, err := dc.ContainerState(ctx, container)
	if err != nil {
		return err
	}
	if !current.Found {
		return fmt.Errorf("the old database container %s is gone", container)
	}
	if !current.Running {
		if err := runLostBy(ctx); err != nil {
			return err
		}
		if err := provisioner.StartHoldingPort(ctx, dc, restoreStartPoller, container); err != nil {
			return err
		}
	}
	if err := u.waitTCP(ctx, dc, container); err != nil {
		return err
	}
	if err := runLostBy(ctx); err != nil {
		return err
	}
	if _, err := runScript(ctx, dc, container, provisioner.UnfenceScript()); err != nil {
		return fmt.Errorf("lift the fence: %w", err)
	}
	if _, err := runScript(ctx, dc, container, provisioner.CleanupScript()); err != nil {
		log.Printf("major upgrade: remove the dump from %s: %v", container, err)
	}
	return nil
}

func (u *DockerMajorUpgrader) waitTCP(ctx context.Context, dc provisioner.DockerClient, container string) error {
	deadline := u.now().Add(u.timings.Start)
	for {
		if err := runLostBy(ctx); err != nil {
			return err
		}
		if code, err := dc.ExecInContainer(ctx, container, tcpReady); err == nil && code == 0 {
			return nil
		}
		if !u.now().Before(deadline) {
			return fmt.Errorf("postgres in %s did not start within %s", container, u.timings.Start)
		}
		if err := u.sleep(ctx, u.timings.Poll); err != nil {
			return err
		}
	}
}

// Recover ends a run whose driver died before the conversion was recorded:
// a recorded new container that loaded the dump and holds the new major is
// the conversion done, and is recorded; anything else puts the old
// container back. See MajorUpgrader.
func (u *DockerMajorUpgrader) Recover(ctx context.Context, inst *domain.DatabaseInstance, run domain.MajorUpgrade, progress UpgradeProgress) UpgradeOutcome {
	dc, renamer, err := u.engine()
	if err != nil || renamer == nil {
		return UpgradeOutcome{Public: notPutBackPublic, Cause: errors.Join(err, errors.New(noRenameBlocker))}
	}
	state := &swap{inst: inst, run: run, oldID: inst.Namespace}
	pre, err := dc.ContainerState(ctx, provisioner.PreUpgradeContainerName(inst.ProjectID, run.ID))
	if err != nil {
		return UpgradeOutcome{Public: notPutBackPublic, Cause: err}
	}
	if pre.Found {
		state.oldID, state.renamed = pre.ID, true
		named, err := dc.ContainerState(ctx, provisioner.DatabaseContainerName(inst.ProjectID))
		if err != nil {
			return UpgradeOutcome{Public: notPutBackPublic, Cause: err}
		}
		if named.Found && named.ID != pre.ID {
			state.newID = named.ID
		}
		state.recorded = inst.Namespace != pre.ID
	}
	if state.newID != "" && inst.Namespace == state.newID && u.holdsNewMajor(ctx, dc, state.newID, run.ToVersion) {
		if err := progress.Converted(ctx, u.now()); err != nil {
			return notRecorded(err)
		}
		return UpgradeOutcome{Converted: true}
	}
	return u.undo(ctx, dc, renamer, state, progress, errors.New("the upgrade was interrupted"))
}

// holdsNewMajor: the new container, started if it is stopped, loaded the
// dump in full and its data directory is on major.
func (u *DockerMajorUpgrader) holdsNewMajor(ctx context.Context, dc provisioner.DockerClient, container, major string) bool {
	current, err := dc.ContainerState(ctx, container)
	if err != nil || !current.Found {
		return false
	}
	if !current.Running {
		if err := provisioner.StartHoldingPort(ctx, dc, restoreStartPoller, container); err != nil {
			log.Printf("major upgrade: start %s to check it: %v", container, err)
			return false
		}
	}
	loaded, err := runScript(ctx, dc, container, provisioner.UpgradeStateScript())
	if err != nil || strings.TrimSpace(loaded) != provisioner.UpgradeLoaded {
		return false
	}
	onDisk, err := runScript(ctx, dc, container, provisioner.DataMajorScript())
	if err != nil || strings.TrimSpace(onDisk) != major {
		return false
	}
	return u.waitTCP(ctx, dc, container) == nil
}

// WaitServing lets the network in to the new server and waits for it. See
// MajorUpgrader.
func (u *DockerMajorUpgrader) WaitServing(ctx context.Context, inst *domain.DatabaseInstance) error {
	dc, _, err := u.engine()
	if err != nil {
		return err
	}
	current, err := dc.ContainerState(ctx, inst.Namespace)
	if err != nil {
		return err
	}
	if current.Found && !current.Running {
		if err := provisioner.StartHoldingPort(ctx, dc, restoreStartPoller, inst.Namespace); err != nil {
			return err
		}
	}
	if err := u.waitTCP(ctx, dc, inst.Namespace); err != nil {
		return err
	}
	if _, err := runScript(ctx, dc, inst.Namespace, provisioner.UnfenceScript()); err != nil {
		return fmt.Errorf("lift the fence: %w", err)
	}
	return nil
}

// AfterSteps updates extensions and builds statistics. See MajorUpgrader.
func (u *DockerMajorUpgrader) AfterSteps(ctx context.Context, inst *domain.DatabaseInstance) []string {
	dc, _, err := u.engine()
	if err == nil {
		_, err = runScript(ctx, dc, inst.Namespace, afterUpgradeScript)
	}
	if err != nil {
		log.Printf("major upgrade %s: after steps: %v", inst.ProjectID, err)
		return []string{afterStepsWarning}
	}
	return nil
}

// Cleanup removes the old container and the dump once the run is over. See
// MajorUpgrader.
func (u *DockerMajorUpgrader) Cleanup(ctx context.Context, inst *domain.DatabaseInstance, run domain.MajorUpgrade) {
	dc, _, err := u.engine()
	if err != nil {
		return
	}
	if err := dc.RemoveContainer(ctx, provisioner.PreUpgradeContainerName(inst.ProjectID, run.ID)); err != nil {
		log.Printf("major upgrade %s: remove the old container: %v", inst.ProjectID, err)
	}
	if _, err := runScript(ctx, dc, inst.Namespace, provisioner.CleanupScript()); err != nil {
		log.Printf("major upgrade %s: remove the dump: %v", inst.ProjectID, err)
	}
}
