package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A changed password does not end the sessions opened with the old one, and
// a replica learns of the change only when it replays it (EXC-675). Rotation
// therefore ends the role's sessions on the primary, then on each replica once
// it has replayed past the change, so no copy keeps one alive.

// replayWaitSeconds bounds how long a replica may take to replay the change.
const replayWaitSeconds = 30

// walPosition is an LSN as pg_current_wal_lsn prints it.
var walPosition = regexp.MustCompile(`^[0-9A-F]{1,8}/[0-9A-F]{1,8}$`)

// sessionPsql runs a script as the superuser in the maintenance database:
// pg_stat_activity covers every database, and the customer cannot set options on this one.
var sessionPsql = []string{"psql", "-X", "-q", "-tA", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-f", "-"}

// SetFeatures gives the service the dark-feature switches it consults.
func (s *ProvisioningService) SetFeatures(flags features.Flags) {
	s.flags = flags
}

// terminateRoleSessionsSQL ends every session the role holds, other than the caller's.
func terminateRoleSessionsSQL(role string) string {
	return fmt.Sprintf("SELECT count(pg_catalog.pg_terminate_backend(pid, %d)) FROM pg_catalog.pg_stat_activity "+
		"WHERE usename = %s AND pid <> pg_catalog.pg_backend_pid();\n", terminateWaitMillis, sqlTextLiteral(role))
}

// awaitReplaySQL fails unless the standby replays up to position within the bound.
func awaitReplaySQL(position string) string {
	return fmt.Sprintf(`DO $excalibase_await_replay$
BEGIN
  FOR attempt IN 1..%[1]d LOOP
    EXIT WHEN pg_catalog.pg_last_wal_replay_lsn() >= '%[2]s'::pg_lsn;
    PERFORM pg_catalog.pg_sleep(0.5);
  END LOOP;
  IF NOT coalesce(pg_catalog.pg_last_wal_replay_lsn() >= '%[2]s'::pg_lsn, false) THEN
    RAISE EXCEPTION 'the replica has not replayed the password change';
  END IF;
END
$excalibase_await_replay$;
`, replayWaitSeconds*2, position)
}

// endsSessionsOnRotation reports whether a rotation ends the old sessions.
func (s *ProvisioningService) endsSessionsOnRotation(ctx context.Context) bool {
	return features.Enabled(ctx, s.flags, features.DBReadRole)
}

// endRoleSessions ends the sessions a role opened with its old password, on
// every copy of the project's database.
func (s *ProvisioningService) endRoleSessions(ctx context.Context, inst *domain.DatabaseInstance, username string) error {
	if inst.DBType == domain.MySQL {
		mysql, err := s.mysqlServerOf()
		if err != nil {
			return err
		}
		return mysql.EndUserSessions(ctx, inst.Namespace, inst.ProjectID, username)
	}
	if inst.DeploymentMode == domain.ModeDocker {
		if s.dockerClient == nil {
			return errors.New("no container engine to reach the database")
		}
		_, err := s.dockerClient.ExecInContainerStdin(ctx, inst.Namespace, sessionPsql, terminateRoleSessionsSQL(username))
		return err
	}
	return s.endClusterRoleSessions(ctx, inst, username)
}

// endClusterRoleSessions ends the sessions on the primary, reading how far it
// has written, then on each replica once it has replayed that far.
func (s *ProvisioningService) endClusterRoleSessions(ctx context.Context, inst *domain.DatabaseInstance, username string) error {
	if s.k8sClient == nil {
		return errors.New("no cluster client to reach the database")
	}
	cluster, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, inst.ProjectID+postgresClusterSuffix)
	if err != nil {
		return fmt.Errorf("read cluster of %s: %w", inst.ProjectID, err)
	}
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if primary == "" {
		return fmt.Errorf("cluster of %s reports no primary", inst.ProjectID)
	}
	out, err := s.k8sClient.ExecInPodStdin(ctx, inst.Namespace, primary, "postgres", sessionPsql,
		terminateRoleSessionsSQL(username)+"SELECT pg_catalog.pg_current_wal_lsn();\n")
	if err != nil {
		return fmt.Errorf("end sessions on %s: %w", primary, err)
	}
	position := lastOutputLine(out)
	if !walPosition.MatchString(position) {
		return fmt.Errorf("the primary %s reported no write position", primary)
	}
	instances, _, _ := unstructured.NestedStringSlice(cluster.Object, "status", "instanceNames")
	for _, pod := range instances {
		if pod == primary {
			continue
		}
		if _, err := s.k8sClient.ExecInPodStdin(ctx, inst.Namespace, pod, "postgres", sessionPsql,
			awaitReplaySQL(position)+terminateRoleSessionsSQL(username)); err != nil {
			return fmt.Errorf("end sessions on replica %s: %w", pod, err)
		}
	}
	return nil
}

func lastOutputLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
