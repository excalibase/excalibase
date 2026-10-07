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
	if !cfg.AppHostingEnabled {
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
	return nil
}

func appRoute(cfg config.AppConfig) k8s.AppRouteOptions {
	return k8s.AppRouteOptions{
		Domain:               cfg.AppDomain,
		IngressClass:         cfg.AppIngressClass,
		Issuer:               cfg.AppDomainIssuer,
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
func registryCredentials(vc vaultclient.VaultClient, instances storage.InstanceStore, kube k8s.KubeClient) *service.RegistryCredentialService {
	if vc == nil {
		return nil
	}
	return service.NewRegistryCredentialService(vc, instances, kube)
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
