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
// terminating the customer backends that held a lock on it.
const roleSQLRetries = 3

// runSuperuserRoleSQL probes the server version so the connection can pin the
// right settings, runs the scripts with the session pinned, and on a failure
// terminates the non-platform backends holding locks on the excalibase schema
// and tries again. A failure that is not a held lock terminates nothing and
// surfaces unchanged on the last attempt.
func runSuperuserRoleSQL(ctx context.Context, exec roleSQLExecer, dbName string, scripts ...string) error {
	versionNum := probeServerVersionNum(ctx, exec, dbName)
	cmd := superuserPsqlCommand(dbName, versionNum, scripts...)
	var err error
	for attempt := 0; attempt < roleSQLRetries; attempt++ {
		if _, err = exec(ctx, cmd); err == nil {
			return nil
		}
		if attempt < roleSQLRetries-1 {
			if _, termErr := exec(ctx, terminateReservedSchemaBlockersCommand(dbName)); termErr != nil {
				log.Printf("role SQL: could not clear schema lock holders for %s: %v", dbName, termErr)
			}
		}
	}
	return err
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
