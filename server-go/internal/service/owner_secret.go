package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// clusterOwnerSecretName is the Secret CloudNativePG keeps the owner login in.
func clusterOwnerSecretName(projectID string) string { return projectID + "-postgres-app" }

// syncClusterOwnerSecret files the owner's password in the cluster's own owner
// Secret. CloudNativePG applies that Secret's password to the owner role each
// time an instance manager starts as primary, so after a failover the role
// holds whatever the Secret holds. A password the customer was given that is
// not in the Secret is replaced by the one from creation at the first failover.
// Only Postgres on Kubernetes has such a Secret; a Secret filed under another
// login is left alone.
func (s *ProvisioningService) syncClusterOwnerSecret(ctx context.Context, inst *domain.DatabaseInstance, username, password string) error {
	if inst.DBType == domain.MySQL || inst.DeploymentMode == domain.ModeDocker || s.k8sClient == nil {
		return nil
	}
	name := clusterOwnerSecretName(inst.ProjectID)
	secret, err := s.k8sClient.GetSecret(ctx, inst.Namespace, name)
	if err != nil {
		return fmt.Errorf("read the owner secret of %s: %w", inst.ProjectID, err)
	}
	if string(secret["username"]) != username {
		return fmt.Errorf("the owner secret of %s is filed under another login than the owner, so a failover would restore the wrong password", inst.ProjectID)
	}
	old := string(secret["password"])
	updated := make(map[string][]byte, len(secret))
	for key, value := range secret {
		updated[key] = value
		if rebuilt, ok := withPasswordReplaced(key, string(value), username, old, password); ok {
			updated[key] = []byte(rebuilt)
		}
	}
	updated["password"] = []byte(password)
	if err := s.k8sClient.UpdateSecret(ctx, inst.Namespace, name, updated); err != nil {
		return fmt.Errorf("file the owner password of %s in its cluster secret: %w", inst.ProjectID, err)
	}
	return nil
}

// withPasswordReplaced rewrites a connection value CloudNativePG derives from
// the password the way it encodes one: userinfo for the URIs, a query value for
// the JDBC URLs, an escaped last field for pgpass. ok is false for a key that
// carries no password.
func withPasswordReplaced(key, value, username, old, password string) (string, bool) {
	if old == "" {
		return value, false
	}
	switch key {
	case "uri", "fqdn-uri":
		return strings.Replace(value, encodedUserinfoPassword(username, old), encodedUserinfoPassword(username, password), 1), true
	case "jdbc-uri", "fqdn-jdbc-uri":
		return strings.Replace(value, "password="+url.QueryEscape(old), "password="+url.QueryEscape(password), 1), true
	case "pgpass":
		return strings.Replace(value, ":"+pgpassEscape(old), ":"+pgpassEscape(password), 1), true
	}
	return value, false
}

func encodedUserinfoPassword(username, password string) string {
	return url.UserPassword(username, password).String()
}

var pgpassEscaper = strings.NewReplacer(`\`, `\\`, ":", `\:`)

func pgpassEscape(value string) string { return pgpassEscaper.Replace(value) }
