package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// HelmWait is how long an install or upgrade waits for the release's pods to
// be ready; a caller bounding one with a deadline must allow at least this.
const HelmWait = 5 * time.Minute

// helmUninstallWait is how long removing a release waits.
const helmUninstallWait = 2 * time.Minute

// HelmApplyBudget is the deadline a caller gives ApplyHelmChart: the removal of
// a stuck release, the install after it, and a minute of margin.
const HelmApplyBudget = helmUninstallWait + HelmWait + time.Minute

// ErrReleaseInUse is Helm's refusal to install over a release that exists.
var ErrReleaseInUse = errors.New("a release with that name is still in use")

// helmStep is what ApplyHelmChart does for a release in a given state.
type helmStep int

const (
	helmInstall helmStep = iota
	helmUpgrade
	// helmReinstall removes a release stuck mid-operation, which Helm will not
	// upgrade ("another operation is in progress"), and installs it afresh.
	helmReinstall
)

func helmApplyStepFor(status release.Status, found bool) helmStep {
	switch {
	case !found:
		return helmInstall
	case status.IsPending() || status == release.StatusUninstalling:
		return helmReinstall
	default:
		return helmUpgrade
	}
}

// InstallHelmChart installs a Helm chart into the given namespace.
func (c *Client) InstallHelmChart(ctx context.Context, namespace, releaseName, chartPath string, values map[string]interface{}) error {
	loaded, actionConfig, err := c.loadChart(namespace, chartPath)
	if err != nil {
		return err
	}
	return runHelmInstall(ctx, actionConfig, namespace, releaseName, loaded, values)
}

func runHelmInstall(ctx context.Context, actionConfig *action.Configuration, namespace, releaseName string, loaded *chart.Chart, values map[string]interface{}) error {
	install := action.NewInstall(actionConfig)
	install.Namespace = namespace
	install.ReleaseName = releaseName
	install.Wait = true
	install.Timeout = HelmWait
	install.CreateNamespace = false

	if _, err := install.RunWithContext(ctx, loaded, values); err != nil {
		return fmt.Errorf("install %s: %w", releaseName, err)
	}
	return nil
}

// ApplyHelmChart installs the release, upgrades it when it exists, and
// removes then installs one stuck mid-operation (an upgrade cancelled at its
// deadline leaves it pending, and Helm refuses every later upgrade).
func (c *Client) ApplyHelmChart(ctx context.Context, namespace, releaseName, chartPath string, values map[string]interface{}) error {
	loaded, actionConfig, err := c.loadChart(namespace, chartPath)
	if err != nil {
		return err
	}
	current, err := action.NewGet(actionConfig).Run(releaseName)
	found := err == nil
	if err != nil && !errors.Is(err, driver.ErrReleaseNotFound) {
		return fmt.Errorf("read release %s: %w", releaseName, err)
	}
	var status release.Status
	if found {
		status = current.Info.Status
	}
	switch helmApplyStepFor(status, found) {
	case helmInstall:
		return runHelmInstall(ctx, actionConfig, namespace, releaseName, loaded, values)
	case helmReinstall:
		log.Printf("WARN: helm release %s/%s is %s; removing it to install afresh", namespace, releaseName, status)
		if err := c.UninstallHelmChart(ctx, namespace, releaseName); err != nil {
			return err
		}
		return runHelmInstall(ctx, actionConfig, namespace, releaseName, loaded, values)
	}
	upgrade := action.NewUpgrade(actionConfig)
	upgrade.Namespace = namespace
	upgrade.Wait = true
	upgrade.Timeout = HelmWait
	if _, err := upgrade.RunWithContext(ctx, releaseName, loaded, values); err != nil {
		return fmt.Errorf("upgrade %s: %w", releaseName, err)
	}
	return nil
}

func (c *Client) loadChart(namespace, chartPath string) (*chart.Chart, *action.Configuration, error) {
	actionConfig, err := c.helmActionConfig(namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("init helm config: %w", err)
	}
	loaded, err := loader.Load(chartPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load chart %s: %w", chartPath, err)
	}
	return loaded, actionConfig, nil
}

// UninstallHelmChart removes a Helm release from the given namespace.
func (c *Client) UninstallHelmChart(ctx context.Context, namespace, releaseName string) error {
	actionConfig, err := c.helmActionConfig(namespace)
	if err != nil {
		return fmt.Errorf("init helm config: %w", err)
	}

	uninstall := action.NewUninstall(actionConfig)
	uninstall.Wait = true
	uninstall.Timeout = helmUninstallWait

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
