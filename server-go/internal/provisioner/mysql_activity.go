package provisioner

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// mysqlClientUsers excludes the work MOCO (moco-*), mysqld itself (mysql.*,
// and NULL for its background threads) and the platform's CDC watcher do.
const mysqlClientUsers = "USER IS NOT NULL AND USER NOT LIKE 'moco-%' AND USER NOT LIKE 'mysql.%' AND USER <> '" + MySQLUserWatcher + "'"

// mysqlClientWorkSQL is the statements the clients ran plus the connections
// they opened since mysqld started; both only grow while it runs.
const mysqlClientWorkSQL = "SELECT " +
	"(SELECT COALESCE(SUM(COUNT_STAR), 0) FROM performance_schema.events_statements_summary_by_user_by_event_name WHERE " + mysqlClientUsers + ") + " +
	"(SELECT COALESCE(SUM(TOTAL_CONNECTIONS), 0) FROM performance_schema.users WHERE " + mysqlClientUsers + ")"

// ClientWork is the work the project's clients did, summed over every
// instance because reads may go to replicas (EXC-588). It is read over the
// admin socket, as MOCO's own user, so reading it is not work.
func (p *MySQLProvisioner) ClientWork(ctx context.Context, namespace, projectID string) (int64, error) {
	instances, err := p.clusterInstances(ctx, namespace, projectID)
	if err != nil {
		return 0, err
	}
	cmd := []string{"mysql", "--defaults-extra-file=" + mysqlAdminCnf, "--batch", "--skip-column-names", "-e", mysqlClientWorkSQL}
	var total int64
	for i := 0; i < instances; i++ {
		pod := k8s.MySQLPodName(projectID, int64(i))
		out, err := p.client.ExecInPod(ctx, namespace, pod, mysqlContainer, cmd)
		if err != nil {
			return 0, fmt.Errorf("read client work on %s: %w", pod, err)
		}
		work, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("client work on %s is not a number: %q", pod, strings.TrimSpace(out))
		}
		total += work
	}
	return total, nil
}
