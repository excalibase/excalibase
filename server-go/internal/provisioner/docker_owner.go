package provisioner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
	"github.com/excalibase/provisioning-poc/internal/pgscram"
)

// A single host's project gets the owner login CloudNativePG bootstraps on
// Kubernetes (EXC-604): a plain LOGIN role owning the project's database. Its
// credential is what vault files and apps receive; the container's superuser
// is reached only by the platform, over the socket.

// defaultOwner is the owner when the request names none, as on Kubernetes.
const defaultOwner = "app"

// errReservedOwner refuses an owner name the platform or Postgres holds.
var errReservedOwner = fmt.Errorf("%w (it names a role the platform reserves)", domain.ErrMasterUsername)

// ownerOf is the request's owner login, refused before any container exists
// when it is not a plain name or is one the platform reserves.
func ownerOf(req domain.ProvisioningRequest) (string, error) {
	if req.MasterUsername == "" {
		return defaultOwner, nil
	}
	if err := ValidateOwner(req.MasterUsername); err != nil {
		return "", err
	}
	return req.MasterUsername, nil
}

// ValidateOwner refuses an owner login that is not a plain role name or that
// the platform, Postgres, DocumentDB or CloudNativePG holds: the platform's
// create-if-absent steps would otherwise adopt it as the project's login.
func ValidateOwner(owner string) error {
	if err := domain.ValidateMasterUsername(owner); err != nil {
		return err
	}
	if pgroles.IsReserved(owner) {
		return errReservedOwner
	}
	return nil
}

// ownerSQL creates the owner and gives it the database. The password goes in
// as a SCRAM verifier, so the plaintext is in no statement.
func ownerSQL(owner, database, password string) (string, error) {
	verifier, err := pgscram.Verifier(password)
	if err != nil {
		return "", fmt.Errorf("hash the owner password: %w", err)
	}
	return pgroles.PinnedSearchPath +
		"CREATE ROLE " + pgIdent(owner) + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD " + pgLiteral(verifier) + ";\n" +
		"ALTER DATABASE " + pgIdent(database) + " OWNER TO " + pgIdent(owner) + ";\n", nil
}

// createOwner runs ownerSQL as the superuser over the container's socket and
// returns the owner's new password.
func (p *DockerPostgreSQLProvisioner) createOwner(ctx context.Context, containerID, database, owner string) (string, error) {
	password := NewOwnerPassword()
	statement, err := ownerSQL(owner, database, password)
	if err != nil {
		return "", err
	}
	if _, err := p.docker.ExecInContainerStdin(ctx, containerID, psqlScript(database), statement); err != nil {
		return "", fmt.Errorf("create the owner login %s: %w", owner, err)
	}
	return password, nil
}

// NewOwnerPassword is 256 random bits as hex: customers pass it to psql,
// mongosh and drivers, where a value starting with '-' reads as an option.
func NewOwnerPassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func pgIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func pgLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// superuserNetworkReject keeps the superuser to the socket, where the platform
// reaches it; no password opens it from the network.
const superuserNetworkReject = "host all " + defaultPostgresSuperuser + " all reject"

// rejectSuperuserScript puts superuserNetworkReject first in the official
// image's pg_hba and reloads it. The file is rewritten in place, so it keeps
// its owner, and a line already there is not added twice.
func rejectSuperuserScript() string {
	return `set -eu
hba=$(psql -U ` + defaultPostgresSuperuser + ` -d postgres -tAc 'SHOW hba_file')
line='` + superuserNetworkReject + `'
if ! grep -qxF "$line" "$hba"; then
  { echo "$line"; cat "$hba"; } > "$hba.excalibase"
  cat "$hba.excalibase" > "$hba"
  rm -f "$hba.excalibase"
fi
psql -U ` + defaultPostgresSuperuser + ` -d postgres -tAc 'SELECT pg_reload_conf()' >/dev/null
`
}

// rejectSuperuserOverNetwork applies rejectSuperuserScript in the container.
func (p *DockerPostgreSQLProvisioner) rejectSuperuserOverNetwork(ctx context.Context, containerID string) error {
	if _, err := p.docker.ExecInContainerStdin(ctx, containerID, []string{"sh", "-s"}, rejectSuperuserScript()); err != nil {
		return fmt.Errorf("keep the superuser off the network: %w", err)
	}
	return nil
}
