package domain

import (
	"errors"
	"net"
	"net/url"
)

// ErrPostgresOnly is what a Postgres-only platform feature (the scheduler's
// tables, declared schemas, the project-DB opener) answers for a MySQL project.
var ErrPostgresOnly = errors.New("not available for MySQL projects")

// Vault credential records of a MySQL project carry these keys beside host,
// port, database, username and password (ADR 0041 decision 5). A record
// without an engine is Postgres.
const (
	CredentialEngineKey   = "engine"
	CredentialTLSKey      = "tls"
	CredentialEngineMySQL = "mysql"
	CredentialTLSRequired = "required"
	// CredentialServerCAKey holds the PEM of the CA the project's MySQL server
	// certificate is signed by, named like the Postgres records' root CA.
	CredentialServerCAKey = "sslrootcert"
	// MySQLSSLMode is what every MySQL client of the platform asks for.
	MySQLSSLMode = "VERIFY_IDENTITY"
)

// IsMySQLRecord reports whether a vault credential record is a MySQL login.
func IsMySQLRecord(record map[string]string) bool {
	return record[CredentialEngineKey] == CredentialEngineMySQL
}

// MySQLURL is a mysql:// connection string that verifies the server's
// certificate and name; the client is given the project's CA separately.
func MySQLURL(host, port, username, password, database string) string {
	u := url.URL{
		Scheme:   "mysql",
		User:     url.UserPassword(username, password),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + database,
		RawQuery: "ssl-mode=" + MySQLSSLMode,
	}
	return u.String()
}

// MySQLJDBCURL is the Connector/J URL of the same database, credentials
// passed separately.
func MySQLJDBCURL(host, port, database string) string {
	return "jdbc:mysql://" + net.JoinHostPort(host, port) + "/" + database + "?sslMode=" + MySQLSSLMode
}
