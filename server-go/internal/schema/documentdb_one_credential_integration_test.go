//go:build integration

package schema

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// EXC-409: a DocumentDB project has ONE credential. The same username and
// password must work in psql and in a MongoDB driver, and rotating it must
// move both at once — a project that can be reached one way and not the other
// is exactly the failure this test exists to catch.
//
// It runs against upstream's documentdb-local image, which is the only
// published artefact carrying the extension, the gateway and a Mongo client
// together. That makes it the one place the two protocols can be proved to
// meet, rather than asserted separately and never compared.
//
// Opt-in, because it pulls a large image and starts a gateway:
//
//	EXCALIBASE_DOCUMENTDB_PROTOCOL_TESTS=1 go test -tags integration \
//	  -run TestOneCredentialWorksOverBothProtocols ./internal/schema/

const (
	// protocolTestsEnv opts in.
	protocolTestsEnv = "EXCALIBASE_DOCUMENTDB_PROTOCOL_TESTS"
	// documentDBLocalImage carries the extension, the gateway and mongosh.
	// Upstream publishes no gateway-and-extension pair any other way.
	documentDBLocalImage = "ghcr.io/documentdb/documentdb/documentdb-local:latest"
	// localPostgresPort is the port documentdb-local runs Postgres on.
	localPostgresPort = "9712"
	// localGatewayPort is the port its gateway listens on.
	localGatewayPort = "10260"
	// mongoAdminRole is the membership DocumentDB requires before a role may
	// do anything over the wire protocol. Authentication needs no membership
	// at all — it reads the ordinary SCRAM verifier — so this is the whole of
	// what the platform grants.
	mongoAdminRole = "documentdb_admin_role"
)

func requireProtocolTests(t *testing.T) {
	t.Helper()
	if os.Getenv(protocolTestsEnv) == "" {
		t.Skipf("pulls %s and starts a gateway; run it with %s=1", documentDBLocalImage, protocolTestsEnv)
	}
}

// documentDBLocal starts the image. Everything is driven from inside the
// container: its Postgres listens on localhost only, and its gateway presents
// a self-signed certificate that is trusted nowhere else — so running psql and
// mongosh in there is both the only way and the honest one, because it is
// exactly how the gateway reaches Postgres in production.
func documentDBLocal(ctx context.Context, t *testing.T) testcontainers.Container {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: documentDBLocalImage,
			Cmd:   []string{"--username", "bootstrap_unused", "--password", "Bootstrap-Unused-1234"},
			WaitingFor: wait.ForLog("Gateway ready to accept connections").
				WithStartupTimeout(5 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start %s: %v", documentDBLocalImage, err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	return container
}

// runInContainer executes a command and returns its exit code and output.
func runInContainer(ctx context.Context, container testcontainers.Container, argv []string) (int, string, error) {
	code, reader, err := container.Exec(ctx, argv)
	if err != nil {
		return code, "", err
	}
	out := new(strings.Builder)
	if reader != nil {
		buf := make([]byte, 8192)
		for {
			n, readErr := reader.Read(buf)
			out.Write(buf[:n])
			if readErr != nil {
				break
			}
		}
	}
	return code, out.String(), nil
}

// requireScramForUser makes the container's Postgres actually check this
// user's password over TCP.
//
// documentdb-local trusts loopback connections, so a psql probe would succeed
// with any password at all and prove nothing about rotation. The rule is added
// for the one role under test and placed before the trust line; the gateway
// reaches Postgres as its own user and is deliberately untouched, because it
// connects passwordless by design.
func requireScramForUser(ctx context.Context, t *testing.T, container testcontainers.Container, user string) {
	t.Helper()
	code, hbaPath, err := runInContainer(ctx, container,
		[]string{"psql", "-tA", "-p", localPostgresPort, "-U", "documentdb", "-d", "postgres", "-c", "SHOW hba_file"})
	if err != nil || code != 0 {
		t.Fatalf("locate pg_hba.conf: exit %d err %v\n%s", code, err, hbaPath)
	}
	path := strings.TrimSpace(lastNonEmptyLine(hbaPath))
	rule := fmt.Sprintf("host all %s 127.0.0.1/32 scram-sha-256", user)
	code, out, err := runInContainer(ctx, container, []string{"bash", "-c",
		fmt.Sprintf("printf '%%s\\n' %q > /tmp/hba.new && cat %q >> /tmp/hba.new && cp /tmp/hba.new %q", rule, path, path)})
	if err != nil || code != 0 {
		t.Fatalf("rewrite pg_hba.conf: exit %d err %v\n%s", code, err, out)
	}
	code, out, err = runInContainer(ctx, container,
		[]string{"psql", "-tA", "-p", localPostgresPort, "-U", "documentdb", "-d", "postgres", "-c", "SELECT pg_reload_conf()"})
	if err != nil || code != 0 {
		t.Fatalf("reload pg_hba.conf: exit %d err %v\n%s", code, err, out)
	}
}

// lastNonEmptyLine returns the final meaningful line of psql output.
//
// Docker's exec stream is frame-multiplexed, so every chunk carries an
// eight-byte header that lands in the middle of the text. Anything that is to
// be used as a path has to have those bytes taken out first, or the value
// looks right in a log and is not a path at all.
func lastNonEmptyLine(out string) string {
	printable := strings.Map(func(r rune) rune {
		if r == '\n' || (r >= 0x20 && r < 0x7f) {
			return r
		}
		return -1
	}, out)
	lines := strings.Split(strings.TrimSpace(printable), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// adminSQL runs a statement as the container's own superuser.
func adminSQL(ctx context.Context, t *testing.T, container testcontainers.Container, statement string) {
	t.Helper()
	code, out, err := runInContainer(ctx, container,
		[]string{"psql", "-v", "ON_ERROR_STOP=1", "-p", localPostgresPort, "-U", "documentdb", "-d", "postgres", "-c", statement})
	if err != nil || code != 0 {
		t.Fatalf("admin SQL %q: exit %d err %v\n%s", statement, code, err, out)
	}
}

// mongoPing authenticates as the given credential through the gateway and
// writes a document, so the result covers authentication and authorisation
// rather than only the handshake. mongosh runs inside the container, which is
// where the gateway's self-signed certificate is trusted.
func mongoPing(ctx context.Context, t *testing.T, container testcontainers.Container, user, password, database string) (string, error) {
	t.Helper()
	uri := fmt.Sprintf(
		"mongodb://%s:%s@localhost:%s/?tls=true&tlsAllowInvalidCertificates=true&authMechanism=SCRAM-SHA-256",
		user, password, localGatewayPort)
	script := fmt.Sprintf(`db.getSiblingDB(%q).probe.insertOne({_id:1}); print("MONGO_OK")`, database)
	code, out, err := runInContainer(ctx, container, []string{"mongosh", uri, "--quiet", "--eval", script})
	if err != nil {
		return out, err
	}
	if code != 0 || !strings.Contains(out, "MONGO_OK") {
		return out, fmt.Errorf("mongosh exited %d", code)
	}
	return out, nil
}

// postgresPing connects with the same credential over libpq, as a client
// would: over TCP, with the password, not through the superuser's local
// socket.
func postgresPing(ctx context.Context, t *testing.T, container testcontainers.Container, user, password string) error {
	t.Helper()
	code, out, err := runInContainer(ctx, container, []string{"env", "PGPASSWORD=" + password,
		"psql", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-p", localPostgresPort,
		"-U", user, "-d", "postgres", "-tAc", "SELECT current_user"})
	if err != nil {
		return err
	}
	if code != 0 || !strings.Contains(out, user) {
		return fmt.Errorf("psql exited %d: %s", code, out)
	}
	return nil
}

// The whole claim, in one test: create the project's credential the ordinary
// way, grant it the one membership DocumentDB needs, and reach the SAME
// password over psql and over a Mongo driver.
func TestOneCredentialWorksOverBothProtocols(t *testing.T) {
	requireProtocolTests(t)
	ctx := context.Background()
	container := documentDBLocal(ctx, t)

	const user = "excalibase_app"
	password := randomTestPassword(t)

	// Created exactly as any project's role is — no DocumentDB API involved.
	adminSQL(ctx, t, container, fmt.Sprintf("CREATE ROLE %q WITH LOGIN PASSWORD '%s'", user, password))
	requireScramForUser(ctx, t, container, user)
	// Authentication already works at this point; authorisation does not.
	if out, err := mongoPing(ctx, t, container, user, password, "before_grant"); err == nil {
		t.Errorf("an ungranted role was allowed to write over Mongo: %s", out)
	}

	adminSQL(ctx, t, container, fmt.Sprintf("GRANT %s TO %q", mongoAdminRole, user))

	if err := postgresPing(ctx, t, container, user, password); err != nil {
		t.Errorf("the credential does not work over postgres: %v", err)
	}
	if out, err := mongoPing(ctx, t, container, user, password, "after_grant"); err != nil {
		t.Errorf("the same credential does not work over mongo: %v\n%s", err, out)
	}
}

// Rotation must not leave a project reachable one way and not the other. The
// Mongo password is the ordinary PostgreSQL SCRAM verifier, so ALTER USER
// moves both at once — this proves it rather than assuming it, and would fail
// loudly if a future DocumentDB release kept a copy of its own.
func TestRotatingTheCredentialRotatesBothProtocols(t *testing.T) {
	requireProtocolTests(t)
	ctx := context.Background()
	container := documentDBLocal(ctx, t)

	const user = "excalibase_app"
	oldPassword := randomTestPassword(t)
	newPassword := randomTestPassword(t)

	adminSQL(ctx, t, container, fmt.Sprintf("CREATE ROLE %q WITH LOGIN PASSWORD '%s'", user, oldPassword))
	requireScramForUser(ctx, t, container, user)
	adminSQL(ctx, t, container, fmt.Sprintf("GRANT %s TO %q", mongoAdminRole, user))

	// The rotation the platform already performs, unchanged for DocumentDB.
	adminSQL(ctx, t, container, fmt.Sprintf("ALTER USER %q WITH PASSWORD '%s'", user, newPassword))

	if err := postgresPing(ctx, t, container, user, newPassword); err != nil {
		t.Errorf("postgres refuses the rotated password: %v", err)
	}
	if out, err := mongoPing(ctx, t, container, user, newPassword, "rotated"); err != nil {
		t.Errorf("mongo refuses the rotated password: %v\n%s", err, out)
	}
	// And the superseded one is refused on both, so a rotation really rotated.
	if err := postgresPing(ctx, t, container, user, oldPassword); err == nil {
		t.Error("postgres still accepts the superseded password")
	}
	if out, err := mongoPing(ctx, t, container, user, oldPassword, "superseded"); err == nil {
		t.Errorf("mongo still accepts the superseded password: %s", out)
	}
}

// randomTestPassword mints the password a test authenticates with. Generated
// rather than written down: a literal here is indistinguishable from a real
// credential to anything scanning this repository.
func randomTestPassword(t *testing.T) string {
	t.Helper()
	var buf [18]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("generate test password: %v", err)
	}
	return "Pw-" + base64.RawURLEncoding.EncodeToString(buf[:])
}
