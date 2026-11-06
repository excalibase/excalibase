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
	conn     net.Conn
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
	upgrade := &cancellableUpgrade{upstream: upstream}
	wrapper, err := rest.HTTPWrappersForConfig(config, upgrade)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewSPDYExecutorForTransports(wrapper, upgrade, http.MethodPost, target)
}

// RoundTrip dials, sends the upgrade request and reads the response, closing
// the connection if the request's context ends first.
func (u *cancellableUpgrade) RoundTrip(req *http.Request) (*http.Response, error) {
	req = utilnet.CloneRequest(req)
	req.Header.Add(httpstream.HeaderConnection, httpstream.HeaderUpgrade)
	req.Header.Add(httpstream.HeaderUpgrade, spdy.HeaderSpdy31)
	conn, err := u.upstream.Dial(req)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(req.Context(), func() { _ = conn.Close() })
	defer stop()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		_ = conn.Close()
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	u.conn = conn
	return resp, nil
}

// NewConnection starts SPDY on an upgraded connection. A response that is not an
// upgrade goes to upstream, which turns it into the API server's error.
func (u *cancellableUpgrade) NewConnection(resp *http.Response) (httpstream.Connection, error) {
	if !isSpdyUpgrade(resp) {
		return u.upstream.NewConnection(resp)
	}
	return spdy.NewClientConnectionWithPings(u.conn, execPingPeriod)
}

func isSpdyUpgrade(resp *http.Response) bool {
	connection := strings.ToLower(resp.Header.Get(httpstream.HeaderConnection))
	upgrade := strings.ToLower(resp.Header.Get(httpstream.HeaderUpgrade))
	return resp.StatusCode == http.StatusSwitchingProtocols &&
		strings.Contains(connection, strings.ToLower(httpstream.HeaderUpgrade)) &&
		strings.Contains(upgrade, strings.ToLower(spdy.HeaderSpdy31))
}
