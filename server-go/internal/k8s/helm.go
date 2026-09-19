package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// InstallHelmChart installs a Helm chart into the given namespace.
func (c *Client) InstallHelmChart(ctx context.Context, namespace, releaseName, chartPath string, values map[string]interface{}) error {
	actionConfig, err := c.helmActionConfig(namespace)
	if err != nil {
		return fmt.Errorf("init helm config: %w", err)
	}

	chart, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("load chart %s: %w", chartPath, err)
	}

	install := action.NewInstall(actionConfig)
	install.Namespace = namespace
	install.ReleaseName = releaseName
	install.Wait = true
	install.Timeout = 5 * time.Minute
	install.CreateNamespace = false

	_, err = install.RunWithContext(ctx, chart, values)
	if err != nil {
		return fmt.Errorf("install %s: %w", releaseName, err)
	}
	return nil
}

// UninstallHelmChart removes a Helm release from the given namespace.
func (c *Client) UninstallHelmChart(ctx context.Context, namespace, releaseName string) error {
	actionConfig, err := c.helmActionConfig(namespace)
	if err != nil {
		return fmt.Errorf("init helm config: %w", err)
	}

	uninstall := action.NewUninstall(actionConfig)
	uninstall.Wait = true
	uninstall.Timeout = 2 * time.Minute

	// A release that is already gone is the state the caller asked for, so a
	// retried teardown succeeds instead of stalling on the first step.
	if _, err = uninstall.Run(releaseName); err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return fmt.Errorf("uninstall %s: %w", releaseName, err)
	}
	return nil
}

func (c *Client) helmActionConfig(namespace string) (*action.Configuration, error) {
	flags := genericclioptions.NewConfigFlags(true)
	flags.Namespace = &namespace

	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(flags, namespace, "secret", log.Printf); err != nil {
		return nil, err
	}
	return actionConfig, nil
}
