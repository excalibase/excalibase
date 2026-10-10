package dockerapps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// PauseAppWorkload stops every container of the app and keeps them, so a
// resume brings back exactly this workload; meanwhile its host answers 503.
func (r *Runtime) PauseAppWorkload(ctx context.Context, namespace string, appID string) error {
	list, err := r.listApps(ctx, labelApp, appID)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return k8s.ErrAppNotDeployed
	}
	project, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return err
	}
	if err := r.updateRecord(project, appID, func(record *appRecord) error {
		record.Serving = nil
		return nil
	}); err != nil {
		return err
	}
	for _, summary := range where(list, isUp) {
		if err := r.stop(ctx, summary.ID); err != nil {
			return err
		}
	}
	return nil
}

// WaitForAppPodsGone waits until no container of the app runs; a stopped one holds nothing.
func (r *Runtime) WaitForAppPodsGone(ctx context.Context, _ string, appID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		list, err := r.listApps(ctx, labelApp, appID)
		if err != nil {
			return err
		}
		running := where(list, isUp)
		if len(running) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w after %s: %s", k8s.ErrAppPodsRemain, timeout, nameOf(running[0]))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.opts.PollInterval):
		}
	}
}

// newestDeploy is the containers of the app's latest deploy under its current
// name, as Kubernetes resumes a Deployment's current template.
func (r *Runtime) newestDeploy(ctx context.Context, appID, appName string) (all, newest []container.Summary, err error) {
	all, err = r.listApps(ctx, labelApp, appID)
	if err != nil {
		return nil, nil, err
	}
	named := where(all, func(c container.Summary) bool { return c.Labels[labelAppName] == appName })
	if len(named) == 0 {
		return all, nil, k8s.ErrAppNotDeployed
	}
	deploy, err := r.lastApplied(named)
	if err != nil {
		return all, nil, err
	}
	newest = where(named, func(c container.Summary) bool { return c.Labels[labelDeploy] == deploy })
	if len(newest) == 0 {
		return all, nil, k8s.ErrAppNotDeployed
	}
	if slices.ContainsFunc(all, isUp) {
		return all, newest, k8s.ErrAppNotPaused
	}
	return all, newest, nil
}

// lastApplied is the deploy the app's record names, else the newest container's.
func (r *Runtime) lastApplied(named []container.Summary) (string, error) {
	record, err := r.loadRecord(named[0].Labels[labelProject], named[0].Labels[labelApp])
	if err != nil {
		return "", err
	}
	if record != nil && record.Deploy != "" {
		return record.Deploy, nil
	}
	latest := slices.MaxFunc(named, func(a, b container.Summary) int { return int(a.Created - b.Created) })
	return latest.Labels[labelDeploy], nil
}

func (r *Runtime) PausedAppReplicas(ctx context.Context, _ string, appID, appName string) (int, error) {
	_, newest, err := r.newestDeploy(ctx, appID, appName)
	if err != nil {
		return 0, err
	}
	return len(newest), nil
}

// ResumeAppWorkload starts the newest deploy's containers again at the plan's
// size now, recreating any sized for another plan, and waits until they are
// ready; then the app is served again unless a project deletion withdrew it.
func (r *Runtime) ResumeAppWorkload(ctx context.Context, namespace, appID, appName string, tier domain.TierType, timeout time.Duration) error {
	all, newest, err := r.newestDeploy(ctx, appID, appName)
	if err != nil {
		return err
	}
	size, err := tierSize(tier)
	if err != nil {
		return err
	}
	project := newest[0].Labels[labelProject]
	if err := r.requireProject(namespace, project); err != nil {
		return err
	}
	if err := r.ensureProjectNetwork(ctx, project, namespace, newest[0].Labels[labelPort] != "0"); err != nil {
		return err
	}
	stale := where(all, func(c container.Summary) bool { return c.Labels[labelDeploy] != newest[0].Labels[labelDeploy] })
	if err := r.retire(ctx, stale); err != nil {
		return err
	}
	ids, err := r.resized(ctx, newest, size)
	if err != nil {
		return err
	}
	if err := r.start(ctx, ids); err != nil {
		return err
	}
	if err := r.waitReady(ctx, appName, ids, timeout); err != nil {
		return err
	}
	return r.updateRecord(project, appID, func(record *appRecord) error {
		record.Serving = containerNames(newest)
		return nil
	})
}

func (r *Runtime) waitReady(ctx context.Context, appName string, ids []string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	watch := newReadiness(r)
	for {
		ready, err := watch.check(waitCtx, ids)
		if err != nil {
			return fmt.Errorf("%w: %s %v", k8s.ErrAppRollout, appName, err)
		}
		if ready {
			return nil
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %s did not become ready within %s", k8s.ErrAppRollout, appName, timeout)
		case <-time.After(r.opts.PollInterval):
		}
	}
}

func containerNames(list []container.Summary) []string {
	names := make([]string, 0, len(list))
	for _, summary := range list {
		names = append(names, nameOf(summary))
	}
	return names
}

// size is one plan's container envelope.
type size struct {
	tier                           string
	nanoCPUs, memoryBytes          int64
	cpuRequestMilli, memoryRequest int64
}

func tierSize(tier domain.TierType) (size, error) {
	tc, err := config.GetAppTierConfig(tier)
	if err != nil {
		return size{}, err
	}
	quantities := map[string]resource.Quantity{}
	for what, raw := range map[string]string{"cpu limit": tc.CPULimit, "memory limit": tc.MemoryLimit,
		"cpu request": tc.CPURequest, "memory request": tc.MemoryRequest} {
		quantity, err := resource.ParseQuantity(raw)
		if err != nil {
			return size{}, fmt.Errorf("tier %s %s %q: %w", tier, what, raw, err)
		}
		quantities[what] = quantity
	}
	cpuLimit, memoryLimit := quantities["cpu limit"], quantities["memory limit"]
	cpuRequest, memoryRequest := quantities["cpu request"], quantities["memory request"]
	return size{
		tier:     strings.ToLower(string(tier)),
		nanoCPUs: cpuLimit.MilliValue() * 1_000_000, memoryBytes: memoryLimit.Value(),
		cpuRequestMilli: cpuRequest.MilliValue(), memoryRequest: memoryRequest.Value(),
	}, nil
}

// resized recreates the containers sized for another plan; the rest are kept as they are.
func (r *Runtime) resized(ctx context.Context, list []container.Summary, want size) ([]string, error) {
	ids := make([]string, 0, len(list))
	for _, summary := range list {
		if summary.Labels[labelTier] == want.tier {
			ids = append(ids, summary.ID)
			continue
		}
		id, err := r.recreate(ctx, summary.ID, func(spec *containerSpec) {
			spec.Tier, spec.NanoCPUs, spec.MemoryBytes = want.tier, want.nanoCPUs, want.memoryBytes
			spec.CPURequestMilli, spec.MemoryRequest = want.cpuRequestMilli, want.memoryRequest
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// recreate replaces a stopped container with one changed as asked, under the
// same name; on Kubernetes a resumed pod is likewise a new pod.
func (r *Runtime) recreate(ctx context.Context, id string, change func(*containerSpec)) (string, error) {
	inspect, err := r.engine.ContainerInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", id, err)
	}
	if inspect.Config == nil || inspect.HostConfig == nil {
		return "", fmt.Errorf("read %s: the engine answered no configuration", id)
	}
	spec, replica, err := specFromContainer(inspect)
	if err != nil {
		return "", err
	}
	change(&spec)
	if err := r.remove(ctx, id); err != nil {
		return "", err
	}
	return r.createAppContainer(ctx, spec, replica)
}

// specFromContainer reads back what createAppContainer wrote.
func specFromContainer(inspect container.InspectResponse) (containerSpec, int, error) {
	labels := inspect.Config.Labels
	probe, ok := probeOf(labels)
	numbers := map[string]int64{}
	for _, key := range []string{labelReplica, labelPort, labelCPURequest, labelMemoryRequest} {
		value, err := strconv.ParseInt(labels[key], 10, 64)
		if err != nil {
			ok = false
		}
		numbers[key] = value
	}
	if !ok {
		return containerSpec{}, 0, errors.New("the container's labels do not describe an app")
	}
	return containerSpec{
		Project: labels[labelProject], AppID: labels[labelApp], AppName: labels[labelAppName],
		DeployID: labels[labelDeploy], Tier: labels[labelTier],
		Image: inspect.Config.Image, Args: inspect.Config.Cmd, Env: inspect.Config.Env,
		Port: int(numbers[labelPort]), Probe: probe,
		NanoCPUs: inspect.HostConfig.NanoCPUs, MemoryBytes: inspect.HostConfig.Memory,
		CPURequestMilli: numbers[labelCPURequest], MemoryRequest: numbers[labelMemoryRequest],
		DiskClaim: labels[labelDisk], DiskMountPath: labels[labelDiskMount],
	}, int(numbers[labelReplica]), nil
}

// WithdrawProjectWorkloads is a project deletion: every route goes, then
// every app container stops. The function runtime does not run on a single host.
func (r *Runtime) WithdrawProjectWorkloads(ctx context.Context, namespace string, opts k8s.WithdrawOptions) error {
	project, err := r.opts.Projects.ProjectOf(namespace)
	if err != nil {
		return err
	}
	if opts.AppRoutes {
		records, err := r.projectRecords(project)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := r.updateRecord(project, record.AppID, func(record *appRecord) error {
				record.Withdrawn = true
				return nil
			}); err != nil {
				return err
			}
		}
	}
	list, err := r.listApps(ctx, labelProject, project)
	if err != nil {
		return err
	}
	for _, summary := range where(list, isUp) {
		if err := r.stop(ctx, summary.ID); err != nil {
			return err
		}
	}
	return nil
}

// RestartFunctionRuntime has nothing to restart: functions have no per-project runtime on a single host.
func (r *Runtime) RestartFunctionRuntime(context.Context, string) error { return nil }

// RestoreAppRoute serves a public app at its hostname again, on its running containers.
func (r *Runtime) RestoreAppRoute(ctx context.Context, namespace string, app *apphost.App, _ k8s.AppRouteOptions) error {
	if err := r.requireProject(namespace, app.ProjectID); err != nil {
		return err
	}
	host, port := "", 0
	if !app.Internal {
		var err error
		if host, err = r.opts.Route.Hostname(app.Name, app.ProjectID); err != nil {
			return err
		}
		port = app.Port
		if err := r.ensureProjectNetwork(ctx, app.ProjectID, namespace, true); err != nil {
			return err
		}
	}
	list, err := r.listApps(ctx, labelApp, app.ID, labelAppName, app.Name)
	if err != nil {
		return err
	}
	running := containerNames(where(list, func(c container.Summary) bool { return c.State == container.StateRunning }))
	return r.updateRecord(app.ProjectID, app.ID, func(record *appRecord) error {
		record.AppName, record.Host, record.Port, record.Withdrawn = app.Name, host, port, false
		record.Serving = running
		return nil
	})
}

// SyncAppDomains routes exactly hosts to the app, beside its own hostname.
func (r *Runtime) SyncAppDomains(_ context.Context, namespace string, app *apphost.App, hosts []string, _ k8s.AppDomainOptions) error {
	if err := r.requireProject(namespace, app.ProjectID); err != nil {
		return err
	}
	if err := r.checkHosts(hosts); err != nil {
		return err
	}
	return r.updateRecord(app.ProjectID, app.ID, func(record *appRecord) error {
		record.AppName, record.Domains = app.Name, slices.Clone(hosts)
		if record.Port == 0 && !app.Internal {
			record.Port = app.Port
		}
		return nil
	})
}
