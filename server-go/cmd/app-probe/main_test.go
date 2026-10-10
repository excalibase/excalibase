package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestProbeHTTPPassesOnSuccessAndRedirectOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/moved":
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	port := portOf(t, server.Listener.Addr().String())
	cases := map[string]bool{"/health": true, "/moved": true, "/down": false}
	for path, want := range cases {
		err := probe([]string{"http", port, path}, "127.0.0.1", time.Second)
		if (err == nil) != want {
			t.Errorf("http %s: err = %v, want pass=%v", path, err, want)
		}
	}
}

func TestProbeTCPNeedsAListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portOf(t, listener.Addr().String())
	if err := probe([]string{"tcp", port}, "127.0.0.1", time.Second); err != nil {
		t.Fatalf("listening port: %v", err)
	}
	listener.Close()
	if err := probe([]string{"tcp", port}, "127.0.0.1", time.Second); err == nil {
		t.Fatal("closed port passed")
	}
}

func TestProbeRefusesWhatItCannotRead(t *testing.T) {
	for _, args := range [][]string{nil, {"udp", "53"}, {"tcp", "0"}, {"tcp", "70000"}, {"http", "80"}, {"http", "x", "/"}, {"http", "80", "no-slash"}} {
		if err := probe(args, "127.0.0.1", time.Second); err == nil {
			t.Errorf("%v passed", args)
		}
	}
}
