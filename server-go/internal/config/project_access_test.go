package config

import "testing"

func TestLoadReadsProvisioningsProjectNamespaceIdentity(t *testing.T) {
	t.Setenv("PROJECT_NAMESPACE_ROLE", "excalibase-provisioning-project")
	t.Setenv("PROVISIONING_SERVICE_ACCOUNT", "provisioning-sa")
	t.Setenv("POD_NAMESPACE", "excalibase-platform")
	cfg := Load()
	if cfg.ProjectNamespaceRole != "excalibase-provisioning-project" ||
		cfg.ProvisioningServiceAccount != "provisioning-sa" || cfg.PodNamespace != "excalibase-platform" {
		t.Fatalf("got role %q, account %q, namespace %q",
			cfg.ProjectNamespaceRole, cfg.ProvisioningServiceAccount, cfg.PodNamespace)
	}
}

func TestLoadLeavesAnUnsetProjectNamespaceIdentityEmpty(t *testing.T) {
	t.Setenv("PROJECT_NAMESPACE_ROLE", "")
	t.Setenv("PROVISIONING_SERVICE_ACCOUNT", "")
	t.Setenv("POD_NAMESPACE", "")
	cfg := Load()
	if cfg.ProjectNamespaceRole != "" || cfg.ProvisioningServiceAccount != "" || cfg.PodNamespace != "" {
		t.Fatalf("invented a value: %+v", []string{cfg.ProjectNamespaceRole, cfg.ProvisioningServiceAccount, cfg.PodNamespace})
	}
}
