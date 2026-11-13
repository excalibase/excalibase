package main

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// verifyAppRuntime refuses to host apps on a cluster that cannot sandbox them.
func verifyAppRuntime(ctx context.Context, cfg config.AppConfig, kube k8s.KubeClient) error {
	// A single host's runtime checks its own sandbox when it is built.
	if !cfg.AppHostingEnabled || cfg.ProvisionerMode == "docker" {
		return nil
	}
	if kube == nil {
		return fmt.Errorf("APP_HOSTING_ENABLED needs the RuntimeClass %q, and there is no Kubernetes client to check it", cfg.AppRuntimeClass)
	}
	found, err := kube.RuntimeClassExists(ctx, cfg.AppRuntimeClass)
	if err != nil {
		return fmt.Errorf("check RuntimeClass %q (APP_RUNTIME_CLASS): %w", cfg.AppRuntimeClass, err)
	}
	if !found {
		return fmt.Errorf("RuntimeClass %q (APP_RUNTIME_CLASS) does not exist in the cluster; app hosting cannot run without it", cfg.AppRuntimeClass)
	}
	return requireGVisorHandler(ctx, cfg.AppRuntimeClass, kube)
}

// requireGVisorHandler refuses a RuntimeClass whose handler is not gVisor: a class
// that is only named "gvisor" would run tenant code on the host kernel.
func requireGVisorHandler(ctx context.Context, class string, kube k8s.KubeClient) error {
	placement, err := kube.RuntimeClassPlacement(ctx, class)
	if err != nil {
		return fmt.Errorf("read RuntimeClass %q (APP_RUNTIME_CLASS): %w", class, err)
	}
	if !k8s.IsGVisorHandler(placement.Handler) {
		return fmt.Errorf("RuntimeClass %q (APP_RUNTIME_CLASS) has handler %q, want runsc: tenant code must run under gVisor", class, placement.Handler)
	}
	return nil
}

// verifyFunctionSandbox checks the class tenant functions run under. A missing
// class only warns (the runtime pods stay Pending, which fails closed); a class
// that exists but is not gVisor stops the platform.
func verifyFunctionSandbox(ctx context.Context, cfg config.AppConfig, kube k8s.KubeClient) error {
	if cfg.ProvisionerMode == "docker" || kube == nil {
		return nil
	}
	class := cfg.AppRuntimeClass
	if class == "" {
		class = k8s.DefaultSandboxRuntimeClass
	}
	found, err := kube.RuntimeClassExists(ctx, class)
	if err != nil {
		return fmt.Errorf("check RuntimeClass %q: %w", class, err)
	}
	if !found {
		log.Printf("WARN: RuntimeClass %q does not exist: edge function runtimes stay Pending until it does", class)
		return nil
	}
	return requireGVisorHandler(ctx, class, kube)
}

func appRoute(cfg config.AppConfig) k8s.AppRouteOptions {
	return k8s.AppRouteOptions{
		Domain:               cfg.AppDomain,
		IngressClass:         cfg.AppIngressClass,
		Issuer:               cfg.AppDomainIssuer,
		WildcardTLS:          cfg.AppWildcardTLS,
		IngressFromNamespace: cfg.AppIngressFromNamespace,
		IngressFromLabels:    cfg.AppIngressFromLabels,
	}
}

// edgePeer is the edge functions and apps reach the platform's public hosts through (EXC-558).
func edgePeer(cfg config.AppConfig) k8s.EdgePeer {
	if !cfg.EdgeConfigured() {
		log.Printf("WARN: EDGE_NAMESPACE, EDGE_POD_LABELS and EDGE_POD_PORTS are unset: functions and apps cannot call the platform's public hosts")
		return k8s.EdgePeer{}
	}
	return k8s.EdgePeer{Namespace: cfg.EdgeNamespace, Labels: maps.Clone(cfg.EdgePodLabels), Ports: slices.Clone(cfg.EdgePodPorts)}
}

// wireAppLifecycle shares the lease with every replica and lets a deleted app
// take its secret values with it; with no vault none could have been stored.
func wireAppLifecycle(apps *service.AppDeployService, claimer service.ProjectOperationClaimer, vc vaultclient.VaultClient) {
	if claimer != nil {
		apps.SetOperationClaimer(claimer)
	}
	if vc != nil {
		apps.SetSecretPurger(vc)
	}
}

// registryCredentials is nil without a vault: there is nowhere to keep a credential.
func registryCredentials(vc vaultclient.VaultClient, instances storage.InstanceStore, runtime service.AppRuntime) *service.RegistryCredentialService {
	if vc == nil {
		return nil
	}
	return service.NewRegistryCredentialService(vc, instances, runtime)
}

func newRegistryCredentialHandler(creds *service.RegistryCredentialService) *handler.RegistryCredentialHandler {
	if creds == nil {
		return handler.NewRegistryCredentialHandler(nil)
	}
	return handler.NewRegistryCredentialHandler(creds)
}

// withRegistryCredentials lets a deploy pull with the project's credential for the image's registry.
func withRegistryCredentials(deploys *service.AppDeployService, creds *service.RegistryCredentialService) *service.AppDeployService {
	if creds != nil {
		deploys.SetRegistryCredentials(creds)
	}
	return deploys
}
