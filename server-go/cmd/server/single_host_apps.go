package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/docker/docker/client"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/dockerapps"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// appNetworkReconcile is how soon a recreated edge or database is back on its projects' networks.
const appNetworkReconcile = 30 * time.Second

// renderRuntimeOptedOut fills the renderer's runtime class when the operator
// ran apps without a sandbox; the container runtime itself is then left empty.
const renderRuntimeOptedOut = "default"

// projectScopes maps a project to its database container, its scope on a single host.
type projectScopes struct{ instances storage.InstanceStore }

func (p projectScopes) ProjectOf(namespace string) (string, error) {
	if namespace == "" {
		return "", errors.New("no project has an empty scope")
	}
	all, err := p.instances.FindAll()
	if err != nil {
		return "", fmt.Errorf("list projects: %w", err)
	}
	project := ""
	for _, inst := range all {
		if inst.Namespace != namespace {
			continue
		}
		if project != "" {
			return "", fmt.Errorf("container %s belongs to more than one project", namespace)
		}
		project = inst.ProjectID
	}
	if project == "" {
		return "", fmt.Errorf("no project runs in container %s", namespace)
	}
	return project, nil
}

func (p projectScopes) NamespaceOf(projectID string) (string, error) {
	inst, err := p.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return "", fmt.Errorf("project %s not found", projectID)
	}
	if inst.Namespace == "" {
		return "", fmt.Errorf("project %s has no database container", projectID)
	}
	return inst.Namespace, nil
}

// buildSingleHostAppRuntime runs apps on the Docker or Podman host when
// PROVISIONER_MODE=docker and app hosting is on; anything missing refuses to start.
func buildSingleHostAppRuntime(ctx context.Context, cfg config.AppConfig, docker provisioner.DockerClient,
	instances storage.InstanceStore) (*dockerapps.Runtime, error) {
	if cfg.ProvisionerMode != "docker" || !cfg.AppHostingEnabled {
		return nil, nil
	}
	sdk, ok := docker.(interface{ RawClient() *client.Client })
	if !ok || sdk.RawClient() == nil {
		return nil, errors.New("apps on a single host need the engine the databases run on")
	}
	engine := sdk.RawClient()
	probe, err := os.ReadFile(cfg.SingleHostApps.ProbeBinary)
	if err != nil {
		return nil, fmt.Errorf("APP_PROBE_BINARY: %w", err)
	}
	podman, err := dockerapps.IsPodman(ctx, engine)
	if err != nil {
		return nil, err
	}
	apps := cfg.SingleHostApps
	runtime, err := dockerapps.New(engine, dockerapps.Options{
		NetworkPrefix: apps.NetworkPrefix, VolumePrefix: apps.VolumePrefix,
		EdgeContainer: apps.EdgeContainer, EdgeTLSAddress: apps.EdgeTLSAddress, RoutesDir: apps.EdgeRoutesDir,
		EdgeIssuesLocally: apps.EdgeIssuesLocally(),
		SandboxRuntime:    apps.SandboxRuntime, Egress: apps.Egress, Isolate: podman,
		ProbeBinary: probe, ToolsImage: cfg.AppDiskToolsImage,
		Route: appRenderOptions(cfg).Route.Public(), ReservedHosts: platformHosts(cfg),
		Projects: projectScopes{instances: instances}, MinReady: dockerapps.DefaultMinReady,
	})
	if err != nil {
		return nil, err
	}
	if err := runtime.VerifyEngine(ctx); err != nil {
		return nil, err
	}
	if apps.SandboxRuntime == "" {
		log.Printf("WARN: APP_SANDBOX_RUNTIME=none: apps run without a sandbox, sharing the host's kernel directly")
	}
	return runtime, nil
}

// platformHosts are the platform's own hostnames, never an app's custom domain.
func platformHosts(cfg config.AppConfig) []string {
	var hosts []string
	for _, raw := range []string{cfg.StudioURL, cfg.PublicBaseURL, cfg.FileStorage.Endpoint} {
		if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
			hosts = append(hosts, parsed.Hostname())
		}
	}
	return hosts
}

// appRuntimeFor is what the app services drive: the single host's runtime,
// else the cluster (nil when there is none).
func appRuntimeFor(kube k8s.KubeClient, singleHost *dockerapps.Runtime) service.AppRuntime {
	if singleHost != nil {
		return singleHost
	}
	if kube == nil {
		return nil
	}
	return kube
}

// appRenderOptions renders apps for the cluster, or for the single host's edge.
func appRenderOptions(cfg config.AppConfig) k8s.AppRenderOptions {
	if cfg.ProvisionerMode == "docker" {
		runtime := cfg.SingleHostApps.SandboxRuntime
		if runtime == "" {
			runtime = renderRuntimeOptedOut
		}
		return dockerapps.RenderOptions(cfg.AppDomain, runtime)
	}
	return k8s.AppRenderOptions{
		RuntimeClass: cfg.AppRuntimeClass, ExtraDenyCIDRs: cfg.AppEgressExtraDenyCIDRs, Edge: edgePeer(cfg), Route: appRoute(cfg),
		DiskStorageClass: cfg.TenantStorageClass,
	}
}

// startSingleHostReconcile keeps the edge and the databases on their projects' networks.
func startSingleHostReconcile(runtime *dockerapps.Runtime) {
	if runtime == nil {
		return
	}
	go runtime.KeepReconciled(context.Background(), appNetworkReconcile, func(err error) {
		log.Printf("WARN: app networks: %v", err)
	})
}
