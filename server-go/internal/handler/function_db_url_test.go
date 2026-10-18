package handler

import (
	"net/url"
	"testing"
)

// EXC-636: a password the URL grammar reserves still reaches the function
// as the password, not as part of the host or path.
func TestFunctionDatabaseURLEscapesTheLogin(t *testing.T) {
	const password = "p@ss:w/rd?#%x"
	target := dbTarget{port: "5432", user: "app:user", pass: password, db: "app"}

	parsed, err := url.Parse(target.url("db.internal"))
	if err != nil {
		t.Fatalf("the URL does not parse: %v", err)
	}
	got, _ := parsed.User.Password()
	if parsed.User.Username() != "app:user" || got != password {
		t.Errorf("login = %q / %q", parsed.User.Username(), got)
	}
	if parsed.Host != "db.internal:5432" || parsed.Path != "/app" || parsed.Query().Get("sslmode") != "require" {
		t.Errorf("host %q path %q query %q", parsed.Host, parsed.Path, parsed.RawQuery)
	}
}
