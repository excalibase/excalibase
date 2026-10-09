package provisioner

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/engineproxy"
)

// The engine proxy admits only containers carrying the label the provisioner sets.
func TestEngineProxyChecksTheLabelTheProvisionerSets(t *testing.T) {
	if engineproxy.ManagedLabel != excalibaseLabel {
		t.Fatalf("proxy checks %q, provisioner sets %q", engineproxy.ManagedLabel, excalibaseLabel)
	}
}
