package clientaddr

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantLen int
		wantErr bool
	}{
		{name: "empty trusts nobody", raw: "", wantLen: 0},
		{name: "single cidr", raw: "10.42.0.0/16", wantLen: 1},
		{name: "list with whitespace", raw: " 10.42.0.0/16 , fd00::/8 ", wantLen: 2},
		{name: "bare ipv4 is a host", raw: "10.1.2.3", wantLen: 1},
		{name: "bare ipv6 is a host", raw: "fd00::1", wantLen: 1},
		{name: "garbage is refused", raw: "not-a-cidr", wantErr: true},
		{name: "ipv4 catch-all is refused", raw: "10.42.0.0/16,0.0.0.0/0", wantErr: true},
		{name: "ipv6 catch-all is refused", raw: "::/0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nets, err := ParseTrustedProxies(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err: got %v, wantErr %v", err, tt.wantErr)
			}
			if len(nets) != tt.wantLen {
				t.Errorf("len: got %d, want %d", len(nets), tt.wantLen)
			}
		})
	}
}

func mustTrust(t *testing.T, raw string) []*net.IPNet {
	t.Helper()
	nets, err := ParseTrustedProxies(raw)
	if err != nil {
		t.Fatal(err)
	}
	return nets
}

func TestResolve(t *testing.T) {
	edge := mustTrust(t, "10.42.0.0/16")
	tests := []struct {
		name    string
		remote  string
		xff     []string
		trusted []*net.IPNet
		want    string
	}{
		{"no trusted proxies: forwarded header ignored", "203.0.113.7:4321", []string{"198.51.100.9"}, nil, "203.0.113.7"},
		{"untrusted peer cannot forge its address", "203.0.113.7:4321", []string{"198.51.100.9"}, edge, "203.0.113.7"},
		{"edge peer: its appended hop is the client", "10.42.1.1:80", []string{"198.51.100.9"}, edge, "198.51.100.9"},
		{"edge peer: client-written left entries are never read", "10.42.1.1:80", []string{"1.2.3.4, 198.51.100.9"}, edge, "198.51.100.9"},
		{"edge peer: further trusted hops are skipped", "10.42.1.1:80", []string{"1.2.3.4, 198.51.100.9, 10.42.2.2"}, edge, "198.51.100.9"},
		{"edge peer: header lines are read in order", "10.42.1.1:80", []string{"1.2.3.4", "198.51.100.9"}, edge, "198.51.100.9"},
		{"edge peer: all hops trusted gives the left-most", "10.42.1.1:80", []string{"10.42.3.3, 10.42.2.2"}, edge, "10.42.3.3"},
		{"edge peer: garbage hop falls back to the peer", "10.42.1.1:80", []string{"198.51.100.9, not-an-ip"}, edge, "10.42.1.1"},
		{"edge peer: garbage left of the client is never read", "10.42.1.1:80", []string{"not-an-ip, 198.51.100.9"}, edge, "198.51.100.9"},
		{"edge peer: no header uses the peer", "10.42.1.1:80", nil, edge, "10.42.1.1"},
		{"ipv6 peer", "[2001:db8::1]:443", nil, edge, "2001:db8::1"},
		{"peer without port", "203.0.113.7", nil, edge, "203.0.113.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, line := range tt.xff {
				r.Header.Add("X-Forwarded-For", line)
			}
			if got := Resolve(r, tt.trusted); got != tt.want {
				t.Errorf("Resolve: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMiddlewareRecordsTheResolvedAddress(t *testing.T) {
	var seen string
	h := Middleware(mustTrust(t, "10.42.0.0/16"))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = FromRequest(r)
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.42.1.1:80"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.9")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if seen != "198.51.100.9" {
		t.Errorf("FromRequest behind middleware: got %q, want 198.51.100.9", seen)
	}
}

func TestFromRequestWithoutMiddlewareNeverReadsTheHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:4321"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got := FromRequest(r); got != "203.0.113.7" {
		t.Errorf("FromRequest: got %q, want 203.0.113.7", got)
	}
}
