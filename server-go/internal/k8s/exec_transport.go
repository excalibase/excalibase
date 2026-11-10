package k8s

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const execPingPeriod = 5 * time.Second

// cancellableUpgrade is client-go's SPDY upgrade with one difference: the wait
// for the upgrade response ends with the request's context. Upstream reads it
// with no way out, so an API server holding the upgrade open holds the call and
// its connection for as long as it likes, past any deadline.
type cancellableUpgrade struct {
	upstream *spdy.SpdyRoundTripper
	proxy    func(*http.Request) (*url.URL, error)
	conn     net.Conn
	stop     func() bool
}

func newCancellableExecutor(config *rest.Config, target *url.URL) (remotecommand.Executor, error) {
	tlsConfig, err := rest.TLSConfigFor(config)
	if err != nil {
		return nil, err
	}
	proxy := http.ProxyFromEnvironment
	if config.Proxy != nil {
		proxy = config.Proxy
	}
	upstream, err := spdy.NewRoundTripperWithConfig(spdy.RoundTripperConfig{TLS: tlsConfig, Proxier: proxy, PingPeriod: execPingPeriod})
	if err != nil {
		return nil, err
	}
	upgrade := &cancellableUpgrade{upstream: upstream, proxy: proxy}
	wrapper, err := rest.HTTPWrappersForConfig(config, upgrade)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewSPDYExecutorForTransports(wrapper, upgrade, http.MethodPost, target)
}

// RoundTrip dials, sends the upgrade request and reads the response, closing
// the connection if the request's context ends first. The watch stays on until
// NewConnection, because reading an error answer's body can hang too.
func (u *cancellableUpgrade) RoundTrip(req *http.Request) (*http.Response, error) {
	u.abandon() // a redirect comes back here with the last connection still open
	req = utilnet.CloneRequest(req)
	req.Header.Add(httpstream.HeaderConnection, httpstream.HeaderUpgrade)
	req.Header.Add(httpstream.HeaderUpgrade, spdy.HeaderSpdy31)
	conn, err := u.dial(req)
	if err != nil {
		return nil, err
	}
	u.conn = conn
	u.stop = context.AfterFunc(req.Context(), func() { _ = conn.Close() })
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		u.abandon()
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	return resp, nil
}

// dial ends with the request's context. Upstream honours it except in a proxy's
// CONNECT, whose connection it keeps to itself, so that wait runs aside: if the
// proxy stays silent the goroutine and its socket stay until the proxy answers or
// hangs up. Only a proxied exec past its deadline can leave one behind.
func (u *cancellableUpgrade) dial(req *http.Request) (net.Conn, error) {
	proxyURL, err := u.proxy(req)
	if err != nil {
		return nil, err
	}
	if proxyURL == nil {
		return u.upstream.Dial(req)
	}
	type dialed struct {
		conn net.Conn
		err  error
	}
	result := make(chan dialed)
	go func() {
		conn, err := u.upstream.Dial(req)
		select {
		case result <- dialed{conn, err}:
		case <-req.Context().Done():
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	select {
	case got := <-result:
		return got.conn, got.err
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

// NewConnection starts SPDY on an upgraded connection. A response that is not an
// upgrade goes to upstream, which turns it into the API server's error, and its
// connection has no further use.
func (u *cancellableUpgrade) NewConnection(resp *http.Response) (httpstream.Connection, error) {
	if !isSpdyUpgrade(resp) {
		defer u.abandon()
		return u.upstream.NewConnection(resp)
	}
	u.stop()
	return spdy.NewClientConnectionWithPings(u.conn, execPingPeriod)
}

func (u *cancellableUpgrade) abandon() {
	if u.conn == nil {
		return
	}
	u.stop()
	_ = u.conn.Close()
	u.conn, u.stop = nil, nil
}

func isSpdyUpgrade(resp *http.Response) bool {
	connection := strings.ToLower(resp.Header.Get(httpstream.HeaderConnection))
	upgrade := strings.ToLower(resp.Header.Get(httpstream.HeaderUpgrade))
	return resp.StatusCode == http.StatusSwitchingProtocols &&
		strings.Contains(connection, strings.ToLower(httpstream.HeaderUpgrade)) &&
		strings.Contains(upgrade, strings.ToLower(spdy.HeaderSpdy31))
}
