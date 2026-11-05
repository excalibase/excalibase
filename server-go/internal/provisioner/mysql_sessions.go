package provisioner

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A changed password leaves the sessions opened with the old one running, and
// a replica applies the change only when replication reaches it (EXC-675).

// mysqlReplicaWaitSeconds bounds how long a replica may take to apply the change.
const mysqlReplicaWaitSeconds = 30

// gtidSet is an executed GTID set as the server prints it, newlines removed.
var gtidSet = regexp.MustCompile(`^[0-9A-Fa-f:,\-]*$`)

// MySQLUserSessionsSQL lists the user's sessions other than the caller's.
func MySQLUserSessionsSQL(user string) string {
	return "SELECT ID FROM information_schema.PROCESSLIST WHERE USER = " + mysqlString(user) + " AND ID <> CONNECTION_ID();"
}

// MySQLKillSessionsSQL ends each listed session that is still open; one
// that ended meanwhile is skipped rather than failing the rest.
func MySQLKillSessionsSQL(ids []int64) string {
	var sql strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&sql, "SET @statement = IF(EXISTS(SELECT 1 FROM information_schema.PROCESSLIST WHERE ID = %[1]d), 'KILL %[1]d', 'DO 0');\n"+
			"PREPARE statement FROM @statement;\nEXECUTE statement;\nDEALLOCATE PREPARE statement;\n", id)
	}
	return sql.String()
}

// MySQLExecutedGTIDSQL reads what the server has applied, on one line.
const MySQLExecutedGTIDSQL = "SELECT REPLACE(@@GLOBAL.gtid_executed, '\\n', '');"

// MySQLAwaitGTIDSQL answers 0 once the server has applied set, 1 on timeout.
func MySQLAwaitGTIDSQL(set string) (string, error) {
	if !gtidSet.MatchString(set) {
		return "", fmt.Errorf("not a GTID set: %q", set)
	}
	return fmt.Sprintf("SELECT WAIT_FOR_EXECUTED_GTID_SET('%s', %d);", set, mysqlReplicaWaitSeconds), nil
}

// mysqlQuery runs statements on one server and returns what it printed.
type mysqlQuery func(ctx context.Context, statements string) (string, error)

// endMySQLSessions ends the user's sessions on one server.
func endMySQLSessions(ctx context.Context, query mysqlQuery, user string) error {
	out, err := query(ctx, MySQLUserSessionsSQL(user))
	if err != nil {
		return fmt.Errorf("list the %s sessions: %w", user, err)
	}
	var ids []int64
	for _, field := range strings.Fields(out) {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return fmt.Errorf("a %s session id is not a number: %q", user, field)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := query(ctx, MySQLKillSessionsSQL(ids)); err != nil {
		return fmt.Errorf("end the %s sessions: %w", user, err)
	}
	return nil
}

// EndUserSessions ends the user's sessions on the single host's mysqld.
func (p *DockerMySQLProvisioner) EndUserSessions(ctx context.Context, namespace, _, user string) error {
	return endMySQLSessions(ctx, func(ctx context.Context, statements string) (string, error) {
		return p.run(ctx, namespace, statements)
	}, user)
}

// EndUserSessions ends the user's sessions on the primary, then on each
// replica once it has applied everything the primary had.
func (p *MySQLProvisioner) EndUserSessions(ctx context.Context, namespace, projectID, user string) error {
	cluster, err := p.client.GetCRD(ctx, k8s.MOCOClusterGVR, namespace, k8s.MySQLClusterName(projectID))
	if err != nil {
		return fmt.Errorf("read MySQL cluster: %w", err)
	}
	primary := k8s.MySQLPrimaryPod(cluster)
	executed, err := p.podQuery(namespace, primary)(ctx, MySQLExecutedGTIDSQL)
	if err != nil {
		return fmt.Errorf("read what %s applied: %w", primary, err)
	}
	await, err := MySQLAwaitGTIDSQL(strings.TrimSpace(executed))
	if err != nil {
		return err
	}
	if err := endMySQLSessions(ctx, p.podQuery(namespace, primary), user); err != nil {
		return err
	}
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "replicas")
	for i := int64(0); i < instances; i++ {
		pod := k8s.MySQLPodName(projectID, i)
		if pod == primary {
			continue
		}
		applied, err := p.podQuery(namespace, pod)(ctx, await)
		if err != nil {
			return fmt.Errorf("wait for %s: %w", pod, err)
		}
		if strings.TrimSpace(applied) != "0" {
			return fmt.Errorf("the replica %s has not applied the password change", pod)
		}
		if err := endMySQLSessions(ctx, p.podQuery(namespace, pod), user); err != nil {
			return fmt.Errorf("on %s: %w", pod, err)
		}
	}
	return nil
}

// podQuery runs statements on one instance as MOCO's admin over the socket.
func (p *MySQLProvisioner) podQuery(namespace, pod string) mysqlQuery {
	return func(ctx context.Context, statements string) (string, error) {
		cmd := []string{"mysql", "--defaults-extra-file=" + mysqlAdminCnf, "--batch", "--skip-column-names"}
		return p.client.ExecInPodStdin(ctx, namespace, pod, mysqlContainer, cmd, statements)
	}
}
