package provisioner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
)

// The platform's SQL on a single host's mysqld runs as root over the socket,
// statements on stdin: the engine logs exec arguments, never stdin.

// mysqlRootClient is the mysql client as root over the socket.
func mysqlRootClient() []string {
	return []string{"mysql", "--socket=" + MySQLSocket, "-uroot", "--batch", "--skip-column-names"}
}

// run feeds statements to the root client and returns what it printed.
func (p *DockerMySQLProvisioner) run(ctx context.Context, container, statements string) (string, error) {
	return p.docker.ExecInContainerStdin(ctx, container, mysqlRootClient(), statements)
}

// runSecret runs statements that carry passwords. mysql echoes a failed
// statement, so the error is logged with the secrets scrubbed and reported
// only as a failure.
func (p *DockerMySQLProvisioner) runSecret(ctx context.Context, container, statements string, secrets ...string) error {
	if _, err := p.run(ctx, container, statements); err != nil {
		reported := err.Error()
		for _, secret := range secrets {
			if secret != "" {
				reported = strings.ReplaceAll(reported, secret, "[redacted]")
			}
		}
		log.Printf("WARN: mysql in %s: %s", container, reported)
		return errors.New("the statements were refused or could not be run in the project's mysqld")
	}
	return nil
}

// EnsurePlatformUsers creates the platform's databases and users, or resets
// the passwords of users a restored database brought.
func (p *DockerMySQLProvisioner) EnsurePlatformUsers(ctx context.Context, namespace, _, database string, passwords MySQLPasswords) error {
	err := p.runSecret(ctx, namespace, MySQLPlatformUsersSQL(database, passwords),
		passwords.Admin, passwords.App, passwords.AuthAdmin, passwords.Watcher)
	if err != nil {
		return fmt.Errorf("create the platform users: %w", err)
	}
	return nil
}

// SetUserPassword sets one platform user's password.
func (p *DockerMySQLProvisioner) SetUserPassword(ctx context.Context, namespace, _, user, password string) error {
	if err := p.runSecret(ctx, namespace, MySQLAlterUserSQL(user, password), password); err != nil {
		return fmt.Errorf("set the %s password: %w", user, err)
	}
	return nil
}

// SetUserPasswords sets several platform users' passwords in one batch, and
// takes away excalibase_app's old auth read grant (EXC-628).
func (p *DockerMySQLProvisioner) SetUserPasswords(ctx context.Context, namespace, _ string, passwords map[string]string) error {
	users := make([]string, 0, len(passwords))
	for user := range passwords {
		users = append(users, user)
	}
	sort.Strings(users)
	var statements strings.Builder
	secrets := make([]string, 0, len(users))
	for _, user := range users {
		statements.WriteString(MySQLAlterUserSQL(user, passwords[user]) + "\n")
		secrets = append(secrets, passwords[user])
	}
	// This is the in-place restore's reset; the backup may predate EXC-628.
	statements.WriteString(MySQLAppAuthRevokeSQL + "\n")
	if err := p.runSecret(ctx, namespace, statements.String(), secrets...); err != nil {
		return fmt.Errorf("set the platform users' passwords: %w", err)
	}
	return nil
}

// ClientWork is the work the project's clients did since mysqld started
// (EXC-588), read as root over the socket, which the reading excludes.
func (p *DockerMySQLProvisioner) ClientWork(ctx context.Context, namespace, _ string) (int64, error) {
	out, err := p.run(ctx, namespace, mysqlClientWorkSQL+";")
	if err != nil {
		return 0, fmt.Errorf("read client work in %s: %w", namespace, err)
	}
	work, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("client work in %s is not a number: %q", namespace, strings.TrimSpace(out))
	}
	return work, nil
}
