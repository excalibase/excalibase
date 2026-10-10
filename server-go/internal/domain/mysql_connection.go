package domain

import (
	"net"
	"net/url"
)

// Vault credential records of a MySQL project carry these keys beside host,
// port, database, username and password (ADR 0041 decision 5). A record
// without an engine is Postgres.
const (
	CredentialEngineKey   = "engine"
	CredentialTLSKey      = "tls"
	CredentialEngineMySQL = "mysql"
	CredentialTLSRequired = "required"
)

// IsMySQLRecord reports whether a vault credential record is a MySQL login.
func IsMySQLRecord(record map[string]string) bool {
	return record[CredentialEngineKey] == CredentialEngineMySQL
}

// MySQLURL is a mysql:// connection string that requires TLS.
func MySQLURL(host, port, username, password, database string) string {
	u := url.URL{
		Scheme:   "mysql",
		User:     url.UserPassword(username, password),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + database,
		RawQuery: "ssl-mode=REQUIRED",
	}
	return u.String()
}

// MySQLJDBCURL is the Connector/J URL of the same database, credentials
// passed separately.
func MySQLJDBCURL(host, port, database string) string {
	return "jdbc:mysql://" + net.JoinHostPort(host, port) + "/" + database + "?sslMode=REQUIRED"
}
