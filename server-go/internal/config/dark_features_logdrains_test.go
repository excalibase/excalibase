package config

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-658: a single host's drains read the lines hostlogs stores, so there
// the flag needs hostlogs; Kubernetes reads the chart's Loki and keeps it.
func TestDarkFeatures_LogDrainsOnASingleHostNeedHostLogs(t *testing.T) {
	cases := []struct {
		mode string
		on   []features.Feature
		want bool
	}{
		{"docker", []features.Feature{features.LogDrains}, false},
		{"docker", []features.Feature{features.LogDrains, features.HostLogs}, true},
		{"kubernetes", []features.Feature{features.LogDrains}, true},
		{"", []features.Feature{features.LogDrains}, true},
		{"docker", []features.Feature{features.HostLogs}, false},
	}
	for _, tc := range cases {
		got := AppConfig{ProvisionerMode: tc.mode, DarkFeatures: tc.on}.Features().Enabled(context.Background(), features.LogDrains)
		if got != tc.want {
			t.Errorf("mode %q with %v: logdrains=%v, want %v", tc.mode, tc.on, got, tc.want)
		}
	}
}
