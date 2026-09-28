package k8s

import (
	"errors"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

// A tenant database requires TLS for every login that arrives over the
// network (EXC-410). Traffic through the public LoadBalancer is SNATed, so
// Postgres cannot tell an outside client from a platform one and the rule
// applies to both. Only the in-pod loopback trust for the DocumentDB gateway
// stays a host line.
//
// hostssl alone is not enough: CNPG appends "host all all all
// scram-sha-256" after the user rules, and a plaintext login that matches no
// hostssl line would be let in there. The hostnossl reject lines stop it first.

const (
	hbaHost    = "host"
	hbaHostSSL = "hostssl"
)

const (
	authRoleName    = "auth_admin"
	watcherRoleName = "cdc_watcher"
)

// PlatformCertRoles are the roles pg_hba admits by certificate only.
var PlatformCertRoles = tenantcert.PlatformRoles

// plaintextRejects close CNPG's catch-all to unencrypted logins. "all" as a
// database does not match replication, so that needs a line of its own.
var plaintextRejects = []string{
	"hostnossl all all all reject",
	"hostnossl replication all all reject",
}

// ErrClusterHasNoHBA refuses a cluster whose rules were not rendered by us.
var ErrClusterHasNoHBA = errors.New("cluster has no pg_hba rules to apply the TLS setting to")

func hbaConnectionType(requireTLS bool) string {
	if requireTLS {
		return hbaHostSSL
	}
	return hbaHost
}

// platformCertLogins make the platform's own roles prove themselves with a
// certificate signed by the cluster's client CA, the way CNPG's
// streaming_replica does (EXC-410). The rejects follow so neither a password
// over TLS nor plaintext reaches the operator's catch-all for these roles.
// They are "host" lines, matching TLS and plaintext alike, and the TLS switch
// never rewrites them.
func platformCertLogins() []interface{} {
	lines := []interface{}{
		hbaHostSSL + " all " + appRoleName + " all cert",
		hbaHostSSL + " all " + authRoleName + " all cert",
		hbaHostSSL + " all " + watcherRoleName + " all cert",
		hbaHostSSL + " replication " + watcherRoleName + " all cert",
	}
	for _, role := range PlatformCertRoles {
		lines = append(lines,
			hbaHost+" all "+role+" all reject",
			hbaHost+" replication "+role+" all reject")
	}
	return lines
}

func networkLogins(requireTLS bool) []interface{} {
	lines := append(platformCertLogins(), hbaConnectionType(requireTLS)+" all app all scram-sha-256")
	if requireTLS {
		for _, reject := range plaintextRejects {
			lines = append(lines, reject)
		}
	}
	return lines
}

// isNetworkLogin is a host or hostssl line that is not the loopback trust, not
// a reject (narrowing one to hostssl would let plaintext through it) and not a
// certificate login (cert is only valid on hostssl).
func isNetworkLogin(fields []string) bool {
	if len(fields) < 4 || (fields[0] != hbaHost && fields[0] != hbaHostSSL) {
		return false
	}
	if method := fields[len(fields)-1]; method == "reject" || method == "cert" {
		return false
	}
	address := fields[3]
	return address != "127.0.0.1/32" && address != "::1/128"
}

// SetClusterRequireTLS rewrites the connection type of every network login
// on a CNPG Cluster and adds or drops the plaintext rejects. The operator
// reloads pg_hba without a restart.
func SetClusterRequireTLS(cluster *unstructured.Unstructured, requireTLS bool) error {
	lines, found, err := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_hba")
	if err != nil {
		return err
	}
	if !found || len(lines) == 0 {
		return ErrClusterHasNoHBA
	}
	rewritten := make([]interface{}, 0, len(lines)+len(plaintextRejects))
	for _, line := range lines {
		if slices.Contains(plaintextRejects, line) {
			continue
		}
		fields := strings.Fields(line)
		if isNetworkLogin(fields) {
			fields[0] = hbaConnectionType(requireTLS)
			line = strings.Join(fields, " ")
		}
		rewritten = append(rewritten, line)
	}
	if requireTLS {
		for _, reject := range plaintextRejects {
			rewritten = append(rewritten, reject)
		}
	}
	return unstructured.SetNestedSlice(cluster.Object, rewritten, "spec", "postgresql", "pg_hba")
}

// ClusterRequiresTLS reports whether every network login is hostssl and
// plaintext is rejected before the operator's catch-all.
func ClusterRequiresTLS(cluster *unstructured.Unstructured) bool {
	lines, _, _ := unstructured.NestedStringSlice(cluster.Object, "spec", "postgresql", "pg_hba")
	for _, reject := range plaintextRejects {
		if !slices.Contains(lines, reject) {
			return false
		}
	}
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
