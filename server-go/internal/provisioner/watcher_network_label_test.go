package provisioner

import "testing"

// The platform chart's NATS policy admits a tenant pod only by this label, and
// an app pod cannot carry it: apps are always labelled component=app.
func TestWatcherPodCarriesTheLabelNATSAdmits(t *testing.T) {
	deployment := renderWatcherDeployment(t)
	if got := deployment.Spec.Template.Labels[WatcherComponentLabel]; got != WatcherComponentValue {
		t.Errorf("watcher pod label %s = %q, want %q", WatcherComponentLabel, got, WatcherComponentValue)
	}
	if WatcherComponentLabel != "excalibase.io/component" || WatcherComponentValue != "cdc-watcher" {
		t.Errorf("label %s=%s no longer matches the platform chart's NATS policy", WatcherComponentLabel, WatcherComponentValue)
	}
}
