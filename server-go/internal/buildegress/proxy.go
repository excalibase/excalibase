package buildegress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// dialTimeout bounds reaching a destination.
	dialTimeout = 15 * time.Second
	// tunnelIdle closes a tunnel no byte crossed for this long.
	tunnelIdle = 5 * time.Minute
)

// hopHeaders are the proxy's own, never forwarded upstream.
var hopHeaders = []string{
	"Proxy-Authorization", "Proxy-Connection", "Proxy-Authenticate", "Connection", "Keep-Alive",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// stripHop removes the hop-by-hop headers, and any the Connection header names as such.
func stripHop(header http.Header) {
	for _, listed := range header.Values("Connection") {
		for _, name := range strings.Split(listed, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range hopHeaders {
		header.Del(name)
	}
}

// Logger receives one line per refusal.
type Logger interface {
	Printf(format string, args ...any)
}

// Proxy is the build's forward proxy: CONNECT tunnels and plain HTTP, each
// destination checked by Policy and dialled at the address it checked.
type Proxy struct {
	Policy   Policy
	Resolver Resolver
	// Dial reaches the checked address; nil dials with net.Dialer.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	Log  Logger
	// MaxConnections bounds requests and tunnels open at once; zero is 256.
	MaxConnections int

	slotsOnce sync.Once
	slots     chan struct{}
}

const defaultMaxConnections = 256

// unreachableLog names a destination that passed the policy but did not answer.
const unreachableLog = "could not reach %s: %v"

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.slotsOnce.Do(func() {
		limit := p.MaxConnections
		if limit <= 0 {
			limit = defaultMaxConnections
		}
		p.slots = make(chan struct{}, limit)
	})
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "the build has too many connections open", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		http.Error(w, "this is a forward proxy: send absolute http:// requests or CONNECT", http.StatusBadRequest)
		return
	}
	p.forward(w, r)
}

// target checks host:port, answering the refusal itself when it is one.
func (p *Proxy) target(w http.ResponseWriter, r *http.Request, hostport, defaultPort string) (string, bool) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, defaultPort
	}
	checked, err := p.Policy.Resolve(r.Context(), p.Resolver, host, port)
	if errors.Is(err, ErrRefused) {
		p.Log.Printf("refused %s %s: %v", r.Method, net.JoinHostPort(host, port), err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return "", false
	}
	if err != nil {
		p.Log.Printf(unreachableLog, net.JoinHostPort(host, port), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return "", false
	}
	return checked.String(), true
}

func (p *Proxy) dial(ctx context.Context, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	if p.Dial != nil {
		return p.Dial(ctx, "tcp4", address)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp4", address)
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	address, ok := p.target(w, r, r.URL.Host, "80")
	if !ok {
		return
	}
	transport := &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return p.dial(ctx, address) },
		DisableKeepAlives: true, Proxy: nil, ResponseHeaderTimeout: time.Minute,
	}
	defer transport.CloseIdleConnections()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHop(out.Header)
	resp, err := transport.RoundTrip(out)
	if err != nil {
		p.Log.Printf(unreachableLog, r.URL.Host, err)
		http.Error(w, "the destination did not answer", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	stripHop(resp.Header)
	for name, values := range resp.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	address, ok := p.target(w, r, r.Host, "")
	if !ok {
		return
	}
	upstream, err := p.dial(r.Context(), address)
	if err != nil {
		p.Log.Printf(unreachableLog, r.Host, err)
		http.Error(w, "the destination did not answer", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "tunnels are not supported here", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	splice(client, buffered.Reader, upstream, tunnelIdle)
}

// splice copies both ways until either side ends, or no byte crosses in either direction for idle:
// a long download with a silent client stays open.
func splice(client net.Conn, pending io.Reader, upstream net.Conn, idle time.Duration) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = client.Close()
			_ = upstream.Close()
		})
	}
	var lastActive atomic.Int64
	lastActive.Store(time.Now().UnixNano())
	done := make(chan struct{}, 2)
	pipe := func(dst net.Conn, src io.Reader, srcConn net.Conn) {
		defer func() { done <- struct{}{} }()
		buffer := make([]byte, 32<<10)
		for {
			_ = srcConn.SetReadDeadline(time.Now().Add(idle))
			n, err := src.Read(buffer)
			if n > 0 {
				lastActive.Store(time.Now().UnixNano())
				if _, werr := dst.Write(buffer[:n]); werr != nil {
					return
				}
			}
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() && time.Since(time.Unix(0, lastActive.Load())) < idle {
				continue
			}
			if err != nil {
				return
			}
		}
	}
	go pipe(upstream, pending, client)
	go pipe(client, upstream, upstream)
	<-done
	closeBoth()
	<-done
}
