package projectdb

import "testing"

// A credential value is data, not syntax: a password carrying a space, a
// quote or a backslash must survive into the connection string intact
// rather than terminating the field it sits in.
func TestDSN_EscapesValuesThatWouldOtherwiseEndTheField(t *testing.T) {
	creds := map[string]string{
		"host": "db.internal", "port": "5432", "database": "tenant",
		"username": "app", "password": `p a'ss\word`,
	}
	got, err := DSNFor(creds, Overrides{SSLMode: "require"})
	if err != nil {
		t.Fatalf("DSNFor: %v", err)
	}
	want := `host='db.internal' port='5432' user='app' password='p a\'ss\\word' dbname='tenant' sslmode='require'`
	if got != want {
		t.Errorf("DSN:\n got %s\nwant %s", got, want)
	}
}

// An override host is a port-forward with no TLS, but the mode must be
// stated rather than inferred: a silently downgraded connection is exactly
// the failure the no-fallback rule exists to prevent.
func TestDSN_RefusesToInferATransportSecurityMode(t *testing.T) {
	creds := map[string]string{"host": "h", "port": "1", "database": "d", "username": "u", "password": "p"}
	if _, err := DSNFor(creds, Overrides{Host: "127.0.0.1"}); err == nil {
		t.Fatal("host override with no ssl mode: want a refusal, got none")
	}
	if _, err := DSNFor(creds, Overrides{Host: "127.0.0.1", SSLMode: "disable"}); err != nil {
		t.Fatalf("host override with an explicit mode: %v", err)
	}
}
