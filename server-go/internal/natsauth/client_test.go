package natsauth

import (
	"testing"

	"github.com/nats-io/nats.go"
)

func TestClientOptionsCarriesTheResilienceOptionsWithoutACredential(t *testing.T) {
	// An unauthenticated local/dev bus still has to survive a restart, so
	// the shared options apply even when no credential is configured.
	opts, err := ClientOptions(PrincipalProvisioning, "", testStream)
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if len(opts) != len(BaseOptions(PrincipalProvisioning)) {
		t.Errorf("got %d options for an unauthenticated connection, want the shared options only", len(opts))
	}
}

func TestClientOptionsCarriesCredentialAndInbox(t *testing.T) {
	opts, err := ClientOptions(PrincipalProvisioning, "pw", testStream)
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if want := len(BaseOptions(PrincipalProvisioning)) + 2; len(opts) != want {
		t.Fatalf("got %d options, want %d (shared + credential + inbox prefix)", len(opts), want)
	}
}

func TestClientOptionsRejectsUnknownPrincipal(t *testing.T) {
	if _, err := ClientOptions("svc-attacker", "pw", testStream); err == nil {
		t.Error("ClientOptions accepted an unknown principal")
	}
}

func TestClientOptionsOmitsInboxForPgDog(t *testing.T) {
	// PgDog never issues a request, so the matrix grants it no inbox and
	// the dial options must not set a prefix it cannot subscribe to.
	opts, err := ClientOptions(PrincipalPgDog, "pw", testStream)
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if want := len(BaseOptions(PrincipalPgDog)) + 1; len(opts) != want {
		t.Errorf("got %d options, want %d (shared + credential only)", len(opts), want)
	}
}

// TestBaseOptionsKeepReconnectingThroughAuthFailures is the EXC-414 client
// half: a callout that times out once must not take a service off the bus
// for the life of the process.
func TestBaseOptionsKeepReconnectingThroughAuthFailures(t *testing.T) {
	var applied nats.Options
	for _, opt := range BaseOptions(PrincipalProvisioning) {
		if err := opt(&applied); err != nil {
			t.Fatalf("applying a base option: %v", err)
		}
	}
	if !applied.RetryOnFailedConnect {
		t.Error("RetryOnFailedConnect is off: a publisher built before the bus is up would fail construction")
	}
	if applied.MaxReconnect != -1 {
		t.Errorf("MaxReconnect = %d, want -1 (retry forever)", applied.MaxReconnect)
	}
	if !applied.IgnoreAuthErrorAbort {
		t.Error("IgnoreAuthErrorAbort is off: two consecutive callout timeouts would abort reconnecting for good")
	}
	if applied.DisconnectedErrCB == nil || applied.ReconnectedCB == nil || applied.AsyncErrorCB == nil {
		t.Error("bus state changes are unobserved: no disconnect, reconnect or error handler")
	}
}
