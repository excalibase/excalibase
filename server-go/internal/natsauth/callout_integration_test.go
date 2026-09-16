//go:build integration

package natsauth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	calloutAccountUser = "auth-callout"
	appAccount         = "APP"
	projectA           = "proj-a"
	projectB           = "proj-b"
	connectTimeout     = 5 * time.Second
)

// memoryCredentialStore stands in for the Postgres-backed store. The
// callout's contract with persistence is one method; wiring a database into
// this test would only slow it down.
type memoryCredentialStore struct{ hashes map[string]string }

func (m *memoryCredentialStore) LookupNatsCredentialHash(_ context.Context, principal string) (string, bool, error) {
	hash, ok := m.hashes[principal]
	return hash, ok, nil
}

// calloutEnv is a live NATS server configured for auth_callout with the
// provisioning responder attached.
type calloutEnv struct {
	url       string
	passwords map[string]string
}

// handshakePingRace is the client error raised when the server pings inside
// the connect handshake, where the client accepts only a PONG. auth_callout
// widens that window because the server holds the connection open until the
// responder answers, so a loaded machine hits it. It says nothing about
// whether the principal was authorized.
const handshakePingRace = "expected 'PONG', got 'PING'"

// connectAs dials the server with one principal's minted credential. Only the
// handshake race above is retried; an authorization refusal is returned as-is
// so the deny tests still observe a real denial.
func (e *calloutEnv) connectAs(t *testing.T, principal string) (*nats.Conn, error) {
	t.Helper()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var conn *nats.Conn
		conn, err = nats.Connect(e.url,
			nats.UserInfo(principal, e.passwords[principal]),
			nats.Timeout(connectTimeout),
			nats.MaxReconnects(0),
		)
		if err == nil {
			t.Cleanup(conn.Close)
			return conn, nil
		}
		if !strings.Contains(err.Error(), handshakePingRace) {
			return nil, err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, err
}

// mustConnectAs fails the test if the principal cannot connect at all.
func (e *calloutEnv) mustConnectAs(t *testing.T, principal string) *nats.Conn {
	t.Helper()
	conn, err := e.connectAs(t, principal)
	if err != nil {
		t.Fatalf("%s could not connect: %v", principal, err)
	}
	return conn
}

func setupCalloutEnv(t *testing.T, principals ...string) *calloutEnv {
	t.Helper()
	ctx := context.Background()

	accountKP, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	issuerSeed, _ := accountKP.Seed()
	issuerPub, _ := accountKP.PublicKey()

	store := &memoryCredentialStore{hashes: map[string]string{}}
	env := &calloutEnv{passwords: map[string]string{}}
	for _, principal := range principals {
		password, err := NewPassword()
		if err != nil {
			t.Fatalf("NewPassword: %v", err)
		}
		hash, err := HashPassword(password)
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
		env.passwords[principal] = password
		store.hashes[principal] = hash
	}

	calloutPassword, _ := NewPassword()
	confPath := writeServerConf(t, issuerPub, calloutPassword)

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nats:2.10-alpine",
			ExposedPorts: []string{"4222/tcp"},
			Cmd:          []string{"-c", "/etc/nats/nats.conf"},
			Files: []testcontainers.ContainerFile{{
				HostFilePath:      confPath,
				ContainerFilePath: "/etc/nats/nats.conf",
				FileMode:          0o644,
			}},
			WaitingFor: wait.ForLog("Server is ready").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start nats: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "4222/tcp")
	env.url = fmt.Sprintf("nats://%s:%s", host, port.Port())

	responder, err := NewResponder(store, string(issuerSeed), appAccount, "CDC")
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	calloutConn, err := nats.Connect(env.url, nats.UserInfo(calloutAccountUser, calloutPassword))
	if err != nil {
		t.Fatalf("callout responder connect: %v", err)
	}
	t.Cleanup(calloutConn.Close)
	if err := responder.Start(calloutConn); err != nil {
		t.Fatalf("responder.Start: %v", err)
	}
	t.Cleanup(responder.Close)

	return env
}

// writeServerConf renders the auth_callout server configuration the charts
// ship. Clients are placed in APP; only the responder lives in AUTH and is
// exempt from the callout.
func writeServerConf(t *testing.T, issuerPub, calloutPassword string) string {
	t.Helper()
	conf := fmt.Sprintf(`
listen: 0.0.0.0:4222
jetstream: { store_dir: "/tmp/js" }

accounts {
  AUTH: { users: [ { user: %q, password: %q } ] }
  APP:  { jetstream: enabled }
  SYS:  {}
}
system_account: SYS

authorization {
  auth_callout {
    issuer: %q
    auth_users: [ %q ]
    account: AUTH
  }
}
`, calloutAccountUser, calloutPassword, issuerPub, calloutAccountUser)

	path := filepath.Join(t.TempDir(), "nats.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatalf("write nats.conf: %v", err)
	}
	return path
}

// permissionRecorder collects the permission violations NATS reports
// asynchronously. The client invokes the error handler on its own goroutine,
// so the value is guarded rather than read straight from the test goroutine.
type permissionRecorder struct {
	mu        sync.Mutex
	violation error
}

func (recorder *permissionRecorder) record(err error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.violation = err
}

func (recorder *permissionRecorder) allowed() bool {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.violation == nil
}

// attach installs the recorder as the connection's error handler.
func (recorder *permissionRecorder) attach(conn *nats.Conn) {
	conn.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { recorder.record(err) })
}

// publishAllowed reports whether a publish was accepted. NATS reports
// permission violations asynchronously, so the connection is flushed and
// then checked for the error the server pushed back.
func publishAllowed(t *testing.T, conn *nats.Conn, subject string) bool {
	t.Helper()
	var recorder permissionRecorder
	recorder.attach(conn)
	if err := conn.Publish(subject, []byte("x")); err != nil {
		return false
	}
	if err := conn.Flush(); err != nil {
		return false
	}
	time.Sleep(200 * time.Millisecond)
	return recorder.allowed()
}

func subscribeAllowed(t *testing.T, conn *nats.Conn, subject string) bool {
	t.Helper()
	var recorder permissionRecorder
	recorder.attach(conn)
	sub, err := conn.SubscribeSync(subject)
	if err != nil {
		return false
	}
	defer sub.Unsubscribe()
	if err := conn.Flush(); err != nil {
		return false
	}
	time.Sleep(200 * time.Millisecond)
	return recorder.allowed()
}

// TestCallout_TenantWatcherIsConfinedToItsOwnProject is the EXC-324
// regression: before the callout, any watcher pod could subscribe cdc.> and
// read every other tenant's change stream.
func TestCallout_TenantWatcherIsConfinedToItsOwnProject(t *testing.T) {
	watcherA := TenantWatcherPrincipal(projectA)
	env := setupCalloutEnv(t, watcherA)
	conn := env.mustConnectAs(t, watcherA)

	if !publishAllowed(t, conn, "cdc."+projectA+".public.orders") {
		t.Error("watcher denied publish on its own project subject")
	}
	if publishAllowed(t, conn, "cdc."+projectB+".public.orders") {
		t.Error("watcher published into another tenant's subject")
	}
	if publishAllowed(t, conn, "policies."+projectA+".changed") {
		t.Error("watcher published a policy-change event")
	}

	for _, subject := range []string{"cdc.>", "cdc." + projectB + ".>", "cdc." + projectA + ".>", "policies.>", ">"} {
		if subscribeAllowed(t, conn, subject) {
			t.Errorf("watcher subscribed to %q", subject)
		}
	}
}

func TestCallout_GraphQLReadsEveryProjectButWritesNone(t *testing.T) {
	env := setupCalloutEnv(t, PrincipalGraphQL)
	conn := env.mustConnectAs(t, PrincipalGraphQL)

	if !subscribeAllowed(t, conn, "cdc.>") {
		t.Error("graphql denied subscribe on cdc.>")
	}
	if !subscribeAllowed(t, conn, "policies.>") {
		t.Error("graphql denied subscribe on policies.>")
	}
	if publishAllowed(t, conn, "cdc."+projectA+".public.orders") {
		t.Error("graphql published into a tenant CDC subject")
	}
	if publishAllowed(t, conn, SubjectPgDogReload) {
		t.Error("graphql published a PgDog reload")
	}
}

func TestCallout_PgDogHearsOnlyItsReloadSubject(t *testing.T) {
	env := setupCalloutEnv(t, PrincipalPgDog, PrincipalProvisioning)
	pgdog := env.mustConnectAs(t, PrincipalPgDog)

	if !subscribeAllowed(t, pgdog, SubjectPgDogReload) {
		t.Error("pgdog denied subscribe on its reload subject")
	}
	if subscribeAllowed(t, pgdog, "cdc.>") {
		t.Error("pgdog subscribed to tenant CDC")
	}
	if publishAllowed(t, pgdog, SubjectPgDogReload) {
		t.Error("pgdog published its own reload signal")
	}

	provisioning := env.mustConnectAs(t, PrincipalProvisioning)
	if !publishAllowed(t, provisioning, SubjectPgDogReload) {
		t.Error("provisioning denied publish on the reload subject")
	}
	if subscribeAllowed(t, provisioning, "cdc.>") {
		t.Error("provisioning subscribed to tenant CDC")
	}
}

// TestCallout_UnknownAndAnonymousConnectionsAreRefused proves the callout
// fails closed rather than falling back to an open server.
func TestCallout_UnknownAndAnonymousConnectionsAreRefused(t *testing.T) {
	watcherA := TenantWatcherPrincipal(projectA)
	env := setupCalloutEnv(t, watcherA)

	if conn, err := nats.Connect(env.url, nats.Timeout(connectTimeout), nats.MaxReconnects(0)); err == nil {
		conn.Close()
		t.Error("anonymous connection accepted")
	}

	env.passwords["svc-attacker"] = env.passwords[watcherA]
	if conn, err := env.connectAs(t, "svc-attacker"); err == nil {
		conn.Close()
		t.Error("unknown principal accepted")
	}

	env.passwords[watcherA] = "wrong-password"
	if conn, err := env.connectAs(t, watcherA); err == nil {
		conn.Close()
		t.Error("wrong password accepted")
	}
}

// TestCallout_EndToEndDelivery shows the intended flow still works: the
// tenant watcher publishes on its own subject and graphql receives it.
func TestCallout_EndToEndDelivery(t *testing.T) {
	watcherA := TenantWatcherPrincipal(projectA)
	env := setupCalloutEnv(t, watcherA, PrincipalGraphQL)

	reader := env.mustConnectAs(t, PrincipalGraphQL)
	sub, err := reader.SubscribeSync("cdc.>")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := reader.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	writer := env.mustConnectAs(t, watcherA)
	subject := "cdc." + projectA + ".public.orders"
	if err := writer.Publish(subject, []byte(`{"op":"insert"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("writer flush: %v", err)
	}

	msg, err := sub.NextMsg(3 * time.Second)
	if err != nil {
		t.Fatalf("graphql did not receive the tenant event: %v", err)
	}
	if msg.Subject != subject {
		t.Errorf("subject = %q, want %q", msg.Subject, subject)
	}
}
