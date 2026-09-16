package natsauth

import "testing"

func TestClientOptionsIsEmptyWithoutACredential(t *testing.T) {
	opts, err := ClientOptions(PrincipalProvisioning, "", testStream)
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if len(opts) != 0 {
		t.Errorf("got %d options for an unauthenticated connection, want 0", len(opts))
	}
}

func TestClientOptionsCarriesCredentialAndInbox(t *testing.T) {
	opts, err := ClientOptions(PrincipalProvisioning, "pw", testStream)
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if len(opts) != 2 {
		t.Fatalf("got %d options, want credential + inbox prefix", len(opts))
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
	if len(opts) != 1 {
		t.Errorf("got %d options, want the credential only", len(opts))
	}
}
