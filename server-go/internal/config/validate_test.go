package config

import "testing"

// Docker mode is single-tenant by design (one customer/team/org, prod or dev).
// Cloud mode is the multi-tenant control plane (multi-org, tier enforcement).
// Combining docker + cloud would run a multi-tenant control plane on a runtime
// that has no cross-tenant isolation — refuse it at boot.
func TestValidate_DockerCloudRejected(t *testing.T) {
	c := AppConfig{ProvisionerMode: "docker", DeploymentMode: "cloud"}
	if err := c.Validate(); err == nil {
		t.Error("docker + cloud must be rejected (multi-tenant on a single-tenant runtime)")
	}
}

func TestValidate_AllowedCombos(t *testing.T) {
	ok := []AppConfig{
		{ProvisionerMode: "docker", DeploymentMode: "selfhosted"}, // the intended docker shape
		{ProvisionerMode: "k8s", DeploymentMode: "cloud"},         // the multi-tenant platform
		{ProvisionerMode: "k8s", DeploymentMode: "selfhosted"},    // single-tenant on k8s
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("%s+%s must be allowed, got %v", c.ProvisionerMode, c.DeploymentMode, err)
		}
	}
}
