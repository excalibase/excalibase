package dnscname

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeServer answers CNAME queries from records, like a recursive resolver would.
func fakeServer(t *testing.T, records map[string]string, rcode dnsmessage.RCode) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if query.Unpack(buf[:n]) != nil || len(query.Questions) != 1 {
				continue
			}
			q := query.Questions[0]
			reply := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RCode: rcode}, Questions: query.Questions}
			if target, ok := records[q.Name.String()]; ok && q.Type == dnsmessage.TypeCNAME {
				reply.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(target)},
				}}
			}
			packed, _ := reply.Pack()
			_, _ = conn.WriteTo(packed, addr)
		}
	}()
	return conn.LocalAddr().String()
}

func TestResolver_ReadsTheCNAMERecordItself(t *testing.T) {
	addr := fakeServer(t, map[string]string{"shop.example.com.": "Web-P1.Apps.Example.IO."}, dnsmessage.RCodeSuccess)
	r := Resolver{Server: addr, Timeout: time.Second}
	target, err := r.CNAME(context.Background(), "shop.example.com")
	if err != nil || target != "web-p1.apps.example.io" {
		t.Fatalf("CNAME = %q, %v", target, err)
	}
	target, err = r.CNAME(context.Background(), "nothing.example.com")
	if err != nil || target != "" {
		t.Fatalf("no CNAME = %q, %v", target, err)
	}
}

func TestResolver_Failures(t *testing.T) {
	servfail := fakeServer(t, nil, dnsmessage.RCodeServerFailure)
	if _, err := (Resolver{Server: servfail, Timeout: time.Second}).CNAME(context.Background(), "shop.example.com"); err == nil {
		t.Fatal("a failed lookup must not read as no CNAME")
	}
	nxdomain := fakeServer(t, nil, dnsmessage.RCodeNameError)
	if target, err := (Resolver{Server: nxdomain, Timeout: time.Second}).CNAME(context.Background(), "gone.example.com"); err != nil || target != "" {
		t.Fatalf("a name that does not exist has no CNAME: %q %v", target, err)
	}
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	if _, err := (Resolver{Server: silent.LocalAddr().String(), Timeout: 100 * time.Millisecond}).CNAME(context.Background(), "shop.example.com"); err == nil {
		t.Fatal("no answer must be an error")
	}
	if _, err := (Resolver{Server: "127.0.0.1:1", Timeout: time.Second}).CNAME(context.Background(), "bad host!"); err == nil {
		t.Fatal("an invalid name must be refused")
	}
}

func TestSystemServer(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(conf, []byte("# comment\nsearch svc.cluster.local\nnameserver 10.43.0.10\nnameserver 1.1.1.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := SystemServer(conf)
	if err != nil || server != "10.43.0.10:53" {
		t.Fatalf("SystemServer = %q, %v", server, err)
	}
	empty := filepath.Join(dir, "empty.conf")
	_ = os.WriteFile(empty, []byte("search x\n"), 0o600)
	if _, err := SystemServer(empty); err == nil || !strings.Contains(err.Error(), "nameserver") {
		t.Fatalf("no nameserver: %v", err)
	}
	if _, err := SystemServer(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("an unreadable file must be an error")
	}
}
