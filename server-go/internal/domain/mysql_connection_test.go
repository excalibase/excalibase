package domain

import "testing"

// ADR 0041 decision 5: MySQL logins are passwords over TLS.
func TestMySQLURL(t *testing.T) {
	got := MySQLURL("moco-p-mysql-primary.ns.svc.cluster.local", "3306", "admin", "p@ss/w:rd", "app")
	want := "mysql://admin:p%40ss%2Fw%3Ard@moco-p-mysql-primary.ns.svc.cluster.local:3306/app?ssl-mode=REQUIRED"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestMySQLJDBCURL(t *testing.T) {
	got := MySQLJDBCURL("db.example.com", "3306", "app")
	if got != "jdbc:mysql://db.example.com:3306/app?sslMode=REQUIRED" {
		t.Errorf("got %s", got)
	}
}

func TestIsMySQLRecord(t *testing.T) {
	if !IsMySQLRecord(map[string]string{"engine": "mysql"}) || IsMySQLRecord(map[string]string{"host": "h"}) {
		t.Error("the engine field decides; no engine is Postgres")
	}
}
