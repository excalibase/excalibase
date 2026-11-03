package projectdb

import (
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// pipeConn is one end of an in-memory connection whose other end never
// answers unless the test writes to it.
func pipeConn(t *testing.T, timeout time.Duration) (*deadlineConn, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return &deadlineConn{Conn: client, timeout: timeout}, server
}

func TestDeadlineConnFailsAReadOnASilentServer(t *testing.T) {
	conn, _ := pipeConn(t, 50*time.Millisecond)
	started := time.Now()
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read on a silent server: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("read failed only after %s", elapsed)
	}
}

func TestDeadlineConnFailsAWriteNobodyReads(t *testing.T) {
	conn, _ := pipeConn(t, 50*time.Millisecond)
	if _, err := conn.Write([]byte("x")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write nobody reads: %v", err)
	}
}

func TestDeadlineConnPassesDataThrough(t *testing.T) {
	conn, server := pipeConn(t, time.Second)
	go func() { _, _ = server.Write([]byte("ok")) }()
	buf := make([]byte, 2)
	if n, err := conn.Read(buf); err != nil || string(buf[:n]) != "ok" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
}

// lib/pq bounds login with a deadline of its own; the per-read bound must
// not push it back out, and clearing it must leave the per-read bound.
func TestDeadlineConnKeepsASoonerDeadlineTheDriverSet(t *testing.T) {
	conn, _ := pipeConn(t, time.Hour)
	for name, set := range map[string]func(time.Time) error{
		"SetDeadline": conn.SetDeadline, "SetReadDeadline": conn.SetReadDeadline,
	} {
		_ = set(time.Now())
		if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("%s in the past: read got %v", name, err)
		}
		_ = conn.SetDeadline(time.Time{})
	}
	_ = conn.SetWriteDeadline(time.Now())
	if _, err := conn.Write([]byte("x")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("SetWriteDeadline in the past: write got %v", err)
	}
}

func TestBoundedDialerWrapsWhatItDials(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := boundedDialer{timeout: 7 * time.Second}.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if bounded, ok := conn.(*deadlineConn); !ok || bounded.timeout != 7*time.Second {
		t.Fatalf("dialled connection is not bounded: %T", conn)
	}
}

func TestOpenPostgresHidesTheConnectionString(t *testing.T) {
	_, err := OpenPostgres("host='db' password='hunter2", time.Second)
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("invalid DSN: %v", err)
	}
}

func TestDSNForBoundsConnectAndLogin(t *testing.T) {
	creds := map[string]string{"host": "h", "port": "1", "database": "d", "username": "u", "password": "p"}
	dsn, err := DSNFor(creds, Overrides{SSLMode: "require"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "connect_timeout='10'") {
		t.Fatalf("dsn %q has no connect timeout", dsn)
	}
}

// A pooled statement is bounded server-side by statement_timeout; the read
// outlives it so the server's own error answers a slow statement.
func TestPoolIOTimeoutOutlivesTheStatementTimeout(t *testing.T) {
	limits := PoolLimits{StatementTimeout: 30 * time.Second}.withDefaults()
	if got := limits.ioTimeout(); got != 40*time.Second {
		t.Fatalf("pool io timeout = %s", got)
	}
}

func TestMySQLPoolBoundsReadsAndWrites(t *testing.T) {
	creds := mysqlCreds()["projects/proj_my/credentials/admin"]
	config, err := MySQLConfigFor(creds, Overrides{}, PoolLimits{StatementTimeout: 30 * time.Second}.withDefaults(), false)
	if err != nil {
		t.Fatal(err)
	}
	if config.ReadTimeout != 40*time.Second || config.WriteTimeout != 40*time.Second {
		t.Fatalf("read %s write %s", config.ReadTimeout, config.WriteTimeout)
	}
	if config.Timeout != ConnectTimeout {
		t.Fatalf("connect timeout %s", config.Timeout)
	}
}
