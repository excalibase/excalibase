package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A Mongo user's pg_ident line (EXC-427). DocumentDB connects back over the
// unix socket as the acting user, and the postgres OS user may only do that
// for roles pg_ident maps. One line per user, because the "+group" form needs
// Postgres 16 and DocumentDB runs on 15. CloudNativePG writes the file and
// reloads; a create waits until the primary has the line and reloads again,
// so the user works the moment its password is handed out.

// identWait bounds that wait. The zero value means the defaults.
type identWait struct {
	timeout  time.Duration
	interval time.Duration
}

const (
	defaultIdentTimeout  = 2 * time.Minute
	defaultIdentInterval = 2 * time.Second
	// peerLineAttempts bounds read-modify-write retries against the operator's own writes.
	peerLineAttempts = 5
)

func (w identWait) orDefaults() identWait {
	if w.timeout <= 0 {
		w.timeout = defaultIdentTimeout
	}
	if w.interval <= 0 {
		w.interval = defaultIdentInterval
	}
	return w
}

// mapMongoUser adds the user's peer line and waits until the primary serves it.
// A single host's container trusts its own socket (provisioner.DocumentDBHBA),
// which only its postgres user reaches, so there is no line to add.
func (s *ProvisioningService) mapMongoUser(ctx context.Context, inst *domain.DatabaseInstance, primary, username string) error {
	if inst.DeploymentMode == domain.ModeDocker {
		return nil
	}
	if err := s.setMongoUserPeerLine(ctx, inst, username, true); err != nil {
		return err
	}
	return s.waitForPeerLine(ctx, inst, primary, username)
}

// undoMongoUser takes back a user the create could not finish. Each step is
// attempted; what fails is logged, and a retry of the same name finds either
// nothing or a role the database refuses to create twice.
func (s *ProvisioningService) undoMongoUser(ctx context.Context, inst *domain.DatabaseInstance, primary, username string) {
	if err := s.execMongoUserSQL(ctx, inst, primary, dropMongoUserSQL(username)); err != nil {
		log.Printf("Mongo user %s in %s: drop after failed create: %v", username, inst.ProjectID, err)
	}
	if err := s.setMongoUserPeerLine(ctx, inst, username, false); err != nil {
		log.Printf("Mongo user %s in %s: unmap after failed create: %v", username, inst.ProjectID, err)
	}
}

// setMongoUserPeerLine writes the line in or out of the Cluster's pg_ident,
// re-reading the Cluster on each attempt so the operator's own writes win.
func (s *ProvisioningService) setMongoUserPeerLine(ctx context.Context, inst *domain.DatabaseInstance, username string, present bool) error {
	if inst.DeploymentMode == domain.ModeDocker {
		return nil
	}
	var lastErr error
	for range peerLineAttempts {
		cluster, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, inst.ProjectID+postgresClusterSuffix)
		if err != nil {
			return fmt.Errorf("read cluster: %w", err)
		}
		changed, updated, err := k8s.WithMongoUserIdent(cluster, username, present)
		if err != nil || !updated {
			return err
		}
		if lastErr = s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, changed); lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("update cluster peer map: %w", lastErr)
}

// waitForPeerLine polls the primary until its pg_ident file has the line, and
// reloads it there so new connections use it.
func (s *ProvisioningService) waitForPeerLine(ctx context.Context, inst *domain.DatabaseInstance, primary, username string) error {
	wait := s.mongoIdentWait.orDefaults()
	deadline := time.Now().Add(wait.timeout)
	for {
		out, err := s.k8sClient.ExecInPodStdin(ctx, inst.Namespace, primary, "postgres",
			[]string{"psql", "-U", "postgres", "-d", "postgres", "-tA", "-v", "ON_ERROR_STOP=1", "-f", "-"},
			peerLineLoadedSQL(username)+"\n")
		if err == nil && strings.TrimSpace(out) == "t" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("peer line for %s not loaded on %s within %s (last: %q, %v)", username, primary, wait.timeout, strings.TrimSpace(out), err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait.interval):
		}
	}
}

// peerLineLoadedSQL answers t once the file maps postgres to the user, after
// reloading the configuration so the running server has it too.
func peerLineLoadedSQL(username string) string {
	return "SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_ident_file_mappings WHERE map_name = 'local' " +
		"AND sys_name = 'postgres' AND pg_username = " + sqlTextLiteral(username) + " AND error IS NULL) " +
		"THEN pg_reload_conf() ELSE false END;"
}
