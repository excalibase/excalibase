package main

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
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
		TLSSecret:            cfg.AppTLSSecret,
		IngressFromNamespace: cfg.AppIngressFromNamespace,
		IngressFromLabels:    cfg.AppIngressFromLabels,
	}
}
