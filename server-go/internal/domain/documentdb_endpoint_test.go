package domain

import (
	"strings"
	"testing"
)

// EXC-409: a DocumentDB project that publishes publicly gets a Mongo port
// beside its Postgres one. It comes from the same allocator, the same window
// and the same quarantine — the reason a stale client must never arrive at a
// stranger's database does not change with the protocol it speaks.

// The two endpoints of one project are distinct Services, so withdrawing one
// never disturbs the other and neither collides with CNPG's own.
func TestMongoServiceNameIsDistinctFromThePostgresOne(t *testing.T) {
	postgres := DBEndpointServiceName("proj-abc1234567")
	mongo := DBEndpointMongoServiceName("proj-abc1234567")

	if mongo == postgres {
		t.Fatalf("both endpoints would be the same Service: %q", mongo)
	}
	if !strings.HasPrefix(mongo, "proj-abc1234567") {
		t.Errorf("the Service is not scoped to the project: %q", mongo)
	}
}

// A Mongo client is told about TLS with tls=true, not sslmode=. The option is
// spelled differently from libpq's and a customer handed the Postgres spelling
// has a string their driver will reject.
func TestMongoConnectionStringExpressesTLSTheMongoWay(t *testing.T) {
	requireTLS := MongoConnectionString("proj-a.db.example.com", 30111, "documentdb_admin", true)

	if !strings.Contains(requireTLS, "tls=true") {
		t.Errorf("TLS is not expressed as tls=true: %q", requireTLS)
	}
	if strings.Contains(requireTLS, "sslmode") {
		t.Errorf("the string carries libpq's spelling of TLS: %q", requireTLS)
	}
	if !strings.HasPrefix(requireTLS, "mongodb://") {
		t.Errorf("not a MongoDB URI: %q", requireTLS)
	}
}

// The gateway authenticates Mongo clients with SCRAM-SHA-256. Drivers that
// negotiate a mechanism can find it, but naming it removes a round trip and,
// more importantly, tells a customer what they are connecting with.
func TestMongoConnectionStringNamesTheAuthenticationMechanism(t *testing.T) {
	uri := MongoConnectionString("proj-a.db.example.com", 30111, "documentdb_admin", true)
	if !strings.Contains(uri, "authMechanism=SCRAM-SHA-256") {
		t.Errorf("the mechanism is not named: %q", uri)
	}
}

// Like the Postgres string, it names the user and leaves the secret out: the
// endpoint API is a Developer-level surface and the password is handed out
// only by the Admin-level credentials endpoint.
func TestMongoConnectionStringCarriesNoPassword(t *testing.T) {
	uri := MongoConnectionString("proj-a.db.example.com", 30111, "documentdb_admin", true)

	if !strings.Contains(uri, "documentdb_admin@") {
		t.Errorf("the string does not name the user: %q", uri)
	}
	if strings.Contains(uri, ":") && strings.Contains(uri, "documentdb_admin:") {
		t.Errorf("the string carries a password: %q", uri)
	}
}

// Both choices are rendered so a customer can see what turning TLS off costs
// before they turn it off, exactly as the Postgres pair does.
func TestMongoConnectionStringsRenderBothTLSChoices(t *testing.T) {
	set := DBEndpointMongoConnectionStrings("proj-a.db.example.com", 30111, "documentdb_admin")

	if !strings.Contains(set.RequireTLS, "tls=true") {
		t.Errorf("require-TLS string: %q", set.RequireTLS)
	}
	if !strings.Contains(set.AllowPlaintext, "tls=false") {
		t.Errorf("plaintext string: %q", set.AllowPlaintext)
	}
	if set.RequireTLS == set.AllowPlaintext {
		t.Error("both choices render the same string")
	}
}

// The role is what lets one project hold two ports in the one allocator.
func TestDBEndpointRolesAreDistinct(t *testing.T) {
	if DBEndpointRolePostgres == DBEndpointRoleMongo {
		t.Fatal("the two roles are the same value")
	}
	for _, role := range []DBEndpointRole{DBEndpointRolePostgres, DBEndpointRoleMongo} {
		if !role.Valid() {
			t.Errorf("%q is not accepted as a role", role)
		}
	}
	if DBEndpointRole("redis").Valid() {
		t.Error("an unknown role was accepted")
	}
}
