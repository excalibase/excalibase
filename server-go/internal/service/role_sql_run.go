package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/pgroles"
)

// roleSQLExecer runs one psql command in a project's primary database, with
// stdin fed to it when there is any, and returns its combined output. A
// non-zero exit is an error. It is the one seam the role-SQL orchestration
// needs, so the same orchestration drives both the Kubernetes and Docker paths
// and a test's own container.
type roleSQLExecer func(ctx context.Context, cmd []string, stdin string) (string, error)

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
	cmd := superuserPsqlCommand(dbName, versionNum)
	input := superuserScript(scripts...)
	var err error
	for attempt := 0; attempt < roleSQLRetries; attempt++ {
		var out string
		out, err = exec(ctx, cmd, input)
		if err == nil {
			if !strings.Contains(out, pgroles.RoleSQLDoneMarker) {
				return errors.New("role SQL ended before the end of its script; it was rolled back")
			}
			return nil
		}
		if !isLockTimeout(out, err) || attempt == roleSQLRetries-1 {
			return withoutVerifiers(err)
		}
		if _, termErr := exec(ctx, terminateNonServiceBackendsCommand(dbName), ""); termErr != nil {
			log.Printf("role SQL: could not terminate the backends blocking %s: %v", dbName, termErr)
		}
	}
	return withoutVerifiers(err)
}

// verifierText matches a SCRAM verifier and its hex form as role SQL writes it.
// A FATAL cannot be caught in PL/pgSQL, so psql can still print the statement's
// context, and what is returned here can become a project's failure reason.
var verifierText = regexp.MustCompile(`(?i)SCRAM-SHA-256\$[A-Za-z0-9+/=:$]*|` + hex.EncodeToString([]byte("SCRAM-SHA-256$")) + `[0-9a-f]*`)

// withoutVerifiers is err with any verifier in its text taken out. An err that
// holds none is returned as it is, so errors.Is still sees through it.
func withoutVerifiers(err error) error {
	if err == nil || !verifierText.MatchString(err.Error()) {
		return err
	}
	return errors.New(verifierText.ReplaceAllString(err.Error(), redactedMarker))
}

// lockTimeoutError matches a lock_timeout as psql reports it with
// VERBOSITY=verbose: the SQLSTATE straight after the ERROR prefix. Text
// elsewhere in the output (a NOTICE, a message quoting "55P03") never counts.
var lockTimeoutError = regexp.MustCompile(`(?m)ERROR:\s+55P03:`)

// isLockTimeout reports whether a role-SQL failure is a lock_timeout (55P03).
// statement_timeout and transaction_timeout are pinned to 0, so a timeout can
// only be a held lock.
func isLockTimeout(output string, err error) bool {
	if lockTimeoutError.MatchString(output) {
		return true
	}
	return err != nil && lockTimeoutError.MatchString(err.Error())
}

// probeServerVersionNum reads server_version_num with a connect-safe command.
// On any trouble it returns 0 (unknown): the SQL still turns
// transaction_timeout off per transaction, so the only cost is not pinning it
// at connect.
func probeServerVersionNum(ctx context.Context, exec roleSQLExecer, dbName string) int {
	out, err := exec(ctx, serverVersionProbeCommand(dbName), "")
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

// appReachesAuth reports whether excalibase_app can still reach the auth
// schema. An answer that is neither t nor f is an error, never a guess.
func appReachesAuth(ctx context.Context, exec roleSQLExecer, dbName string) (bool, error) {
	out, err := exec(ctx, authAccessProbeCommand(dbName), "")
	if err != nil {
		return false, fmt.Errorf("probe excalibase_app's access to auth: %w", err)
	}
	switch strings.TrimSpace(out) {
	case "t":
		return true, nil
	case "f":
		return false, nil
	}
	return false, fmt.Errorf("probe excalibase_app's access to auth: unexpected answer %q", strings.TrimSpace(out))
}

// roleSQLExecer returns the execer bound to a project's primary database for
// the active deployment mode.
func (s *ProvisioningService) roleSQLExecer(namespace, primaryPod string) roleSQLExecer {
	return func(ctx context.Context, cmd []string, stdin string) (string, error) {
		if s.dockerClient != nil {
			return s.dockerClient.ExecInContainerStdin(ctx, namespace, cmd, stdin)
		}
		return s.k8sClient.ExecInPodStdin(ctx, namespace, primaryPod, "postgres", cmd, stdin)
	}
}
