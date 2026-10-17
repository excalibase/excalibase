package service

import (
	"context"
	"log"
	"strconv"
	"strings"
)

// roleSQLExecer runs one psql command in a project's primary database and
// returns its combined output. A non-zero exit is an error. It is the one
// seam the role-SQL orchestration needs, so the same orchestration drives
// both the Kubernetes and Docker paths and a test's own container.
type roleSQLExecer func(ctx context.Context, cmd []string) (string, error)

// roleSQLRetries bounds how many times the role SQL is re-run after
// terminating the customer backends that blocked it with a lock.
const roleSQLRetries = 3

// runSuperuserRoleSQL probes the server version so the connection can pin the
// right settings, then runs the scripts with the session pinned. A customer
// or owner backend can hold a lock that blocks the rebuild (on an excalibase
// object, a dependent elsewhere, or a catalog row the second script needs);
// the finite lock_timeout turns that into a lock-timeout error, after which
// it terminates every non-service backend — the only ones a customer can be
// behind — and retries. Any other failure terminates nothing and is returned
// at once.
func runSuperuserRoleSQL(ctx context.Context, exec roleSQLExecer, dbName string, scripts ...string) error {
	versionNum := probeServerVersionNum(ctx, exec, dbName)
	cmd := superuserPsqlCommand(dbName, versionNum, scripts...)
	var err error
	for attempt := 0; attempt < roleSQLRetries; attempt++ {
		var out string
		out, err = exec(ctx, cmd)
		if err == nil {
			return nil
		}
		if !isLockTimeout(out, err) || attempt == roleSQLRetries-1 {
			return err
		}
		if _, termErr := exec(ctx, terminateNonServiceBackendsCommand(dbName)); termErr != nil {
			log.Printf("role SQL: could not terminate the backends blocking %s: %v", dbName, termErr)
		}
	}
	return err
}

// isLockTimeout reports whether a role-SQL failure is a lock_timeout (55P03).
// statement_timeout and transaction_timeout are pinned to 0, so a timeout can
// only be a held lock.
func isLockTimeout(output string, err error) bool {
	haystack := strings.ToLower(output)
	if err != nil {
		haystack += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(haystack, "lock timeout") || strings.Contains(haystack, "55p03")
}

// probeServerVersionNum reads server_version_num with a connect-safe command.
// On any trouble it returns 0 (unknown): the SQL still turns
// transaction_timeout off per transaction, so the only cost is not pinning it
// at connect.
func probeServerVersionNum(ctx context.Context, exec roleSQLExecer, dbName string) int {
	out, err := exec(ctx, serverVersionProbeCommand(dbName))
	if err != nil {
		log.Printf("role SQL: could not read server version for %s (continuing): %v", dbName, err)
		return 0
	}
	num, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return num
}

// roleSQLExecer returns the execer bound to a project's primary database for
// the active deployment mode.
func (s *ProvisioningService) roleSQLExecer(namespace, primaryPod string) roleSQLExecer {
	return func(ctx context.Context, cmd []string) (string, error) {
		if s.dockerClient != nil {
			return s.dockerClient.ExecInContainerStdin(ctx, namespace, cmd, "")
		}
		return s.k8sClient.ExecInPod(ctx, namespace, primaryPod, "postgres", cmd)
	}
}
