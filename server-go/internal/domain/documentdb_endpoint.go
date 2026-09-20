package domain

import (
	"net/url"
	"strconv"
)

// The Mongo half of a project's database endpoint (EXC-409).
//
// A DocumentDB project serves two protocols from one pod: Postgres on 5432 and
// the MongoDB wire protocol on the gateway's 10260. When such a project turns
// public access on it gets a port for each, both from the one allocator EXC-410
// built — the same random window and the same quarantine on release. The
// reasoning behind the quarantine does not change with the protocol: a stale
// client dialling a reissued port would arrive at a stranger's database either
// way.
//
// A project that stays private gets neither. Public reach is opt-in for the
// database as a whole, and DocumentDB does not open a second door to it.

// DBEndpointRole says which of a project's two endpoints a port belongs to.
// It is what lets one project hold two ports in one allocator: the allocator's
// table is keyed by project and role, and the uniqueness constraint that stops
// two projects sharing a port spans the whole table, so a Postgres port and a
// Mongo port can never collide either.
type DBEndpointRole string

const (
	// DBEndpointRolePostgres is the port a libpq client dials. Every project
	// that publishes has one.
	DBEndpointRolePostgres DBEndpointRole = "postgres"
	// DBEndpointRoleMongo is the port a MongoDB client dials, which only a
	// DocumentDB project has.
	DBEndpointRoleMongo DBEndpointRole = "mongo"
)

// Valid reports whether this is a role the allocator recognises. An unknown
// role is refused rather than stored: a row under a role nothing reads back is
// a port held forever by nobody.
func (r DBEndpointRole) Valid() bool {
	return r == DBEndpointRolePostgres || r == DBEndpointRoleMongo
}

// DBEndpointMongoServiceName is the project's public Mongo LoadBalancer
// Service. It is separate from the Postgres one so either can be withdrawn
// without disturbing the other — a project can be paused, republished or have
// its endpoint turned off without the two getting out of step — and separate
// from anything CNPG owns.
func DBEndpointMongoServiceName(projectID string) string {
	return projectID + "-documentdb-public"
}

// documentDBAuthMechanism is how the gateway authenticates Mongo clients. It
// is named in the connection string rather than left to the driver to
// negotiate, so a customer can see what they are authenticating with.
const documentDBAuthMechanism = "SCRAM-SHA-256"

// MongoConnectionString renders the URI a MongoDB client dials.
//
// TLS is not spelled the way Postgres spells it: libpq takes sslmode with five
// possible values, a MongoDB driver takes tls as a boolean, and a customer
// handed the Postgres spelling has a string their driver rejects outright.
// Verifying the chain is a separate, client-side matter — drivers take the CA
// as a file (--tlsCAFile), not as a URI option — and the CA to put in that file
// is the one the endpoint API returns alongside this string.
//
// Like the Postgres string it carries no password. The endpoint API is a
// Developer-level surface; the secret is handed out only by the Admin-level
// credentials endpoint.
func MongoConnectionString(host string, port int, user string, requireTLS bool) string {
	uri := url.URL{
		Scheme: "mongodb",
		User:   url.User(user),
		Host:   host + ":" + strconv.Itoa(port),
		Path:   "/",
		RawQuery: url.Values{
			"tls":           []string{strconv.FormatBool(requireTLS)},
			"authMechanism": []string{documentDBAuthMechanism},
		}.Encode(),
	}
	return uri.String()
}

// DBEndpointMongoConnectionStrings renders both TLS choices for one project's
// Mongo endpoint, so a customer can see exactly what turning TLS off costs
// them before they turn it off.
func DBEndpointMongoConnectionStrings(host string, port int, user string) DBEndpointConnectionStringSet {
	return DBEndpointConnectionStringSet{
		RequireTLS:     MongoConnectionString(host, port, user, true),
		AllowPlaintext: MongoConnectionString(host, port, user, false),
	}
}
