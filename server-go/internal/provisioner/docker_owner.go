package provisioner

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
var errReservedOwner = errors.New("masterUsername names a role the platform reserves")

// ownerOf is the request's owner login, refused before any container exists
// when it is not a plain name or is one the platform reserves.
func ownerOf(req domain.ProvisioningRequest) (string, error) {
	owner := req.MasterUsername
	if owner == "" {
		return defaultOwner, nil
	}
	if err := domain.ValidateMasterUsername(owner); err != nil {
		return "", err
	}
	if slices.Contains(pgroles.ReservedNames(), owner) {
		return "", errReservedOwner
	}
	for _, prefix := range pgroles.ReservedPrefixes() {
		if strings.HasPrefix(owner, prefix) {
			return "", errReservedOwner
		}
	}
	return owner, nil
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
	password := generatePassword()
	statement, err := ownerSQL(owner, database, password)
	if err != nil {
		return "", err
	}
	if _, err := p.docker.ExecInContainerStdin(ctx, containerID, psqlScript(database), statement); err != nil {
		return "", fmt.Errorf("create the owner login %s: %w", owner, err)
	}
	return password, nil
}

func pgIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func pgLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
