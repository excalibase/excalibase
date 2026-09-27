package k8s

import (
	"errors"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A tenant database requires TLS for every login that arrives over the
// network (EXC-410). Traffic through the public LoadBalancer is SNATed, so
// Postgres cannot tell an outside client from a platform one and the rule
// applies to both. Only the in-pod loopback trust for the DocumentDB gateway
// stays a host line.

const (
	hbaHost    = "host"
	hbaHostSSL = "hostssl"
)

// ErrClusterHasNoHBA refuses a cluster whose rules were not rendered by us.
var ErrClusterHasNoHBA = errors.New("cluster has no pg_hba rules to apply the TLS setting to")

func hbaConnectionType(requireTLS bool) string {
	if requireTLS {
		return hbaHostSSL
	}
	return hbaHost
}

func networkLogins(connectionType string) []interface{} {
	return []interface{}{
		connectionType + " replication cdc_watcher all scram-sha-256",
		connectionType + " all app all scram-sha-256",
		connectionType + " all " + appRoleName + " all scram-sha-256",
		connectionType + " all auth_admin all scram-sha-256",
	}
}

// isNetworkLogin is a host or hostssl line that is not the loopback trust.
func isNetworkLogin(fields []string) bool {
	if len(fields) < 4 || (fields[0] != hbaHost && fields[0] != hbaHostSSL) {
		return false
	}
	address := fields[3]
	return address != "127.0.0.1/32" && address != "::1/128"
}

// SetClusterRequireTLS rewrites the connection type of every network login
// on a CNPG Cluster. The operator reloads pg_hba without a restart.
func SetClusterRequireTLS(cluster *unstructured.Unstructured, requireTLS bool) error {
	lines, found, err := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_hba")
	if err != nil {
		return err
	}
	if !found || len(lines) == 0 {
		return ErrClusterHasNoHBA
	}
	rewritten := make([]interface{}, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if isNetworkLogin(fields) {
			fields[0] = hbaConnectionType(requireTLS)
			line = strings.Join(fields, " ")
		}
		rewritten = append(rewritten, line)
	}
	return unstructured.SetNestedSlice(cluster.Object, rewritten, "spec", "postgresql", "pg_hba")
}

// ClusterRequiresTLS reports whether every network login is hostssl.
func ClusterRequiresTLS(cluster *unstructured.Unstructured) bool {
	lines, _, _ := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_hba")
	networkLines := 0
	for _, line := range lines {
		fields := strings.Fields(line)
		if !isNetworkLogin(fields) {
			continue
		}
		networkLines++
		if fields[0] != hbaHostSSL {
			return false
		}
	}
	return networkLines > 0
}
