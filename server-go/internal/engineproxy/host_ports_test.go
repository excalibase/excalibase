package engineproxy

import (
	"errors"
	"testing"
)

func withHostPort(t *testing.T, ip, port string) []byte {
	t.Helper()
	return createBody(t, func(b map[string]any) {
		hostConfig(b)["PortBindings"] = map[string]any{"3306/tcp": []any{map[string]any{"HostIp": ip, "HostPort": port}}}
	})
}

// EXC-592: a project keeps its host port across restarts, restores and
// upgrades, so a fixed port is admitted from the operator's tenant range only.
func TestAHostPortIsAdmittedOnlyFromTheTenantRange(t *testing.T) {
	policy := testPolicy()
	policy.HostPorts = HostPortRange{Low: 30000, High: 30999}
	for _, port := range []string{"30000", "30500", "30999", ""} {
		if err := policy.CheckCreate("excalibase-p1-mysql", withHostPort(t, "127.0.0.1", port)); err != nil {
			t.Errorf("port %q refused: %v", port, err)
		}
	}
	for _, port := range []string{"22", "29999", "31000", "443", "3o000", "-1", "30000-30001"} {
		if err := policy.CheckCreate("excalibase-p1-mysql", withHostPort(t, "127.0.0.1", port)); !errors.Is(err, ErrRefused) {
			t.Errorf("port %q admitted: %v", port, err)
		}
	}
	if err := policy.CheckCreate("excalibase-p1-mysql", withHostPort(t, "0.0.0.0", "30001")); !errors.Is(err, ErrRefused) {
		t.Errorf("a range port on a refused address: %v", err)
	}
}

func TestWithoutATenantRangeEveryHostPortIsRefused(t *testing.T) {
	if err := testPolicy().CheckCreate("excalibase-p1-mysql", withHostPort(t, "127.0.0.1", "30001")); !errors.Is(err, ErrRefused) {
		t.Fatalf("admitted: %v", err)
	}
}
