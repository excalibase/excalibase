package service

import "github.com/excalibase/provisioning-poc/internal/domain"

// operatorURLs are the operator releases the live labs install, the same ones
// the chart and setup-k8s.sh pin. The server itself installs nothing (EXC-520).
var operatorURLs = map[domain.DatabaseType]string{
	domain.PostgreSQL: "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.1.yaml",
	// MOCO (ADR 0041); its manifest needs cert-manager installed first.
	domain.MySQL: "https://github.com/cybozu-go/moco/releases/download/v0.37.0/moco.yaml",
}
