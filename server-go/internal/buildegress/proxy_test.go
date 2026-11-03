package buildegress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingDialer sends every dial to one local server and records the address asked for.
type recordingDialer struct {
	mu     sync.Mutex
	asked  []string
	target string
}

func (d *recordingDialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.asked = append(d.asked, network+" "+address)
	d.mu.Unlock()
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", d.target)
}

func (d *recordingDialer) dials() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string{}, d.asked...)
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func startProxy(t *testing.T, upstream string, resolver Resolver) (*httptest.Server, *recordingDialer, *logLines) {
	t.Helper()
	policy := newTestPolicy(t, nil, nil)
	dialer := &recordingDialer{target: upstream}
	logs := &logLines{}
	proxy := &Proxy{Policy: policy, Resolver: resolver, Dial: dialer.dial, Log: logs}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	return server, dialer, logs
}

func upstreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "upstream saw %s %s proxy-auth=%q", r.Method, r.URL.Path, r.Header.Get("Proxy-Authorization"))
	}))
	t.Cleanup(server.Close)
	return server
}

func proxiedClient(proxyURL string) *http.Client {
	parsed, _ := url.Parse(proxyURL)
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(parsed)}}
}

func TestProxyForwardsPlainHTTPToTheCheckedAddress(t *testing.T) {
	upstream := upstreamServer(t)
	resolver := fakeResolver{"public.example": addrs("93.184.216.34")}
	proxy, dialer, _ := startProxy(t, upstream.Listener.Addr().String(), resolver)
	req, _ := http.NewRequest(http.MethodGet, "http://public.example/hello", nil)
	req.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
	resp, err := proxiedClient(proxy.URL).Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "upstream saw GET /hello") {
		t.Fatalf("answer %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "c2VjcmV0") {
		t.Fatal("the proxy forwarded Proxy-Authorization upstream")
	}
	if got := dialer.dials(); len(got) != 1 || got[0] != "tcp4 93.184.216.34:80" {
		t.Fatalf("dials %v, want only the checked address", got)
	}
}

func TestProxyRefusesPlainHTTPToAPrivateAddress(t *testing.T) {
	upstream := upstreamServer(t)
	proxy, dialer, logs := startProxy(t, upstream.Listener.Addr().String(), fakeResolver{})
	resp, err := proxiedClient(proxy.URL).Get("http://169.254.169.254/latest/meta-data/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if len(dialer.dials()) != 0 {
		t.Fatal("the proxy dialled a refused address")
	}
	if !strings.Contains(logs.String(), "169.254.169.254:80") || strings.Contains(logs.String(), "meta-data") {
		t.Fatalf("the refusal log should name host:port only: %q", logs.String())
	}
}

func TestProxyTunnelsConnectToAPublicHost(t *testing.T) {
	upstream := upstreamServer(t)
	resolver := fakeResolver{"registry.npmjs.org": addrs("104.16.1.34")}
	proxy, dialer, _ := startProxy(t, upstream.Listener.Addr().String(), resolver)
	conn := connect(t, proxy, "registry.npmjs.org:443")
	defer conn.Close()
	reader := bufio.NewReader(conn)
	status, _ := reader.ReadString('\n')
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT answered %q", status)
	}
	skipHeaders(reader)
	// The tunnel is a plain byte pipe: speak HTTP through it to the upstream.
	fmt.Fprintf(conn, "GET /through HTTP/1.1\r\nHost: registry.npmjs.org\r\nConnection: close\r\n\r\n")
	answer, _ := io.ReadAll(reader)
	if !strings.Contains(string(answer), "upstream saw GET /through") {
		t.Fatalf("through the tunnel: %q", answer)
	}
	if got := dialer.dials(); len(got) != 1 || got[0] != "tcp4 104.16.1.34:443" {
		t.Fatalf("dials %v", got)
	}
}

func TestProxyRefusesConnectToDeniedTargets(t *testing.T) {
	upstream := upstreamServer(t)
	resolver := fakeResolver{"internal.example": addrs("10.0.0.5"), "mail.example": addrs("93.184.216.34")}
	proxy, dialer, logs := startProxy(t, upstream.Listener.Addr().String(), resolver)
	for _, target := range []string{"internal.example:443", "mail.example:25", "127.0.0.1:443", "[::1]:443"} {
		conn := connect(t, proxy, target)
		status, _ := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if !strings.Contains(status, "403") {
			t.Fatalf("CONNECT %s answered %q", target, status)
		}
	}
	if len(dialer.dials()) != 0 {
		t.Fatalf("dialled %v", dialer.dials())
	}
	if !strings.Contains(logs.String(), "internal.example:443") {
		t.Fatalf("refusals not logged: %q", logs.String())
	}
}

func TestProxyRefusesWhatIsNotAProxyRequest(t *testing.T) {
	upstream := upstreamServer(t)
	proxy, _, _ := startProxy(t, upstream.Listener.Addr().String(), fakeResolver{})
	resp, err := http.Get(proxy.URL + "/local")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an origin-form request answered %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://public.example/", nil)
	if status := rawProxyStatus(t, proxy, req); status != http.StatusBadRequest {
		t.Fatalf("an https absolute URI (not CONNECT) answered %d", status)
	}
}

func TestProxyNamesAFailedLookupAsBadGateway(t *testing.T) {
	upstream := upstreamServer(t)
	proxy, _, _ := startProxy(t, upstream.Listener.Addr().String(), fakeResolver{})
	resp, err := proxiedClient(proxy.URL).Get("http://nowhere.example/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
}

func connect(t *testing.T, proxy *httptest.Server, target string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	return conn
}

func skipHeaders(reader *bufio.Reader) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil || line == "\r\n" {
			return
		}
	}
}

// rawProxyStatus sends req in absolute form, as a client configured with the proxy would.
func rawProxyStatus(t *testing.T, proxy *httptest.Server, req *http.Request) int {
	t.Helper()
	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := req.WriteProxy(conn); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestProxyNamesAnUnreachableDestination(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := closed.Addr().String()
	closed.Close()
	resolver := fakeResolver{"down.example": addrs("93.184.216.34")}
	proxy, _, logs := startProxy(t, address, resolver)
	conn := connect(t, proxy, "down.example:443")
	status, _ := bufio.NewReader(conn).ReadString('\n')
	conn.Close()
	if !strings.Contains(status, "502") {
		t.Fatalf("CONNECT to a closed port answered %q", status)
	}
	resp, err := proxiedClient(proxy.URL).Get("http://down.example/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(logs.String(), "could not reach") {
		t.Fatalf("plain HTTP to a closed port: %d %q", resp.StatusCode, logs.String())
	}
}

// Past its connection cap the proxy answers 503 instead of opening another tunnel.
func TestProxyCapsItsConnections(t *testing.T) {
	upstream := upstreamServer(t)
	resolver := fakeResolver{"example.com": addrs("93.184.216.34")}
	dialer := &recordingDialer{target: strings.TrimPrefix(upstream.URL, "http://")}
	proxy := &Proxy{Policy: newTestPolicy(t, nil, nil), Resolver: resolver, Dial: dialer.dial, Log: &logLines{}, MaxConnections: 1}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	held := connect(t, server, "example.com:443")
	defer held.Close()
	if status, _ := bufio.NewReader(held).ReadString('\n'); !strings.Contains(status, "200") {
		t.Fatalf("first tunnel %q", status)
	}
	second := connect(t, server, "example.com:443")
	defer second.Close()
	if status, _ := bufio.NewReader(second).ReadString('\n'); !strings.Contains(status, "503") {
		t.Fatalf("second tunnel past the cap %q", status)
	}
}

// A download the client listens to in silence outlives the idle window; a tunnel silent both ways does not.
func TestSpliceIdleCountsBothDirections(t *testing.T) {
	clientSide, proxyClient := net.Pipe()
	proxyUpstream, upstreamSide := net.Pipe()
	finished := make(chan struct{})
	go func() {
		splice(proxyClient, proxyClient, proxyUpstream, 100*time.Millisecond)
		close(finished)
	}()
	go func() {
		for range 6 {
			_, _ = upstreamSide.Write([]byte("chunk"))
			time.Sleep(50 * time.Millisecond)
		}
	}()
	received, _ := io.ReadAll(io.LimitReader(clientSide, 30))
	if string(received) != strings.Repeat("chunk", 6) {
		t.Fatalf("client received %q", received)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("a tunnel silent both ways stayed open")
	}
}

func TestStripHopRemovesHeadersTheConnectionNames(t *testing.T) {
	header := http.Header{"Connection": {"X-Private, keep-alive"}, "X-Private": {"secret"}, "Keep-Alive": {"5"}, "Accept": {"*/*"}}
	stripHop(header)
	if len(header) != 1 || header.Get("Accept") != "*/*" {
		t.Fatalf("left %v", header)
	}
}
