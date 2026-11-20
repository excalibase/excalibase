package k8s

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// The build's deadline is what ends a hung exec, since neither the client nor
// the role SQL's session has a timeout of its own: pin that the real client
// returns when its context ends, wherever the API server is holding it.
func TestExecInPod_ReturnsWhenItsContextEnds(t *testing.T) {
	disconnected := make(chan struct{}, 1)
	servers := map[string]func(release <-chan struct{}) http.HandlerFunc{
		"before the upgrade is answered": func(release <-chan struct{}) http.HandlerFunc {
			return func(_ http.ResponseWriter, r *http.Request) {
				// The connection must close too, or the hung read leaks for good.
				select {
				case <-r.Context().Done():
					disconnected <- struct{}{}
				case <-release:
				}
			}
		},
		"streaming, the command never finishing": func(release <-chan struct{}) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if _, err := httpstream.Handshake(r, w, []string{"v4.channel.k8s.io"}); err != nil {
					return
				}
				acceptAll := func(httpstream.Stream, <-chan struct{}) error { return nil }
				conn := spdy.NewResponseUpgrader().UpgradeResponse(w, r, acceptAll)
				if conn == nil {
					return
				}
				defer conn.Close()
				<-release
			}
		},
	}
	for name, server := range servers {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			api := httptest.NewServer(server(release))
			defer api.Close()
			defer close(release)
			client, err := NewClientWith(ClientOptions{APIURL: api.URL, BearerToken: "x"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			returned := make(chan error, 1)
			go func() {
				_, err := client.ExecInPodStdin(ctx, "ns", "pod", "c", []string{"true"}, "SELECT 1")
				returned <- err
			}()

			select {
			case err := <-returned:
				if err == nil {
					t.Fatal("exec reported success against a server that never finished")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("exec did not return after its context ended")
			}
			if name == "before the upgrade is answered" {
				select {
				case <-disconnected:
				case <-time.After(5 * time.Second):
					t.Fatal("the hung handshake's connection stayed open")
				}
			}
		})
	}
}

// An answer that is not an upgrade ends the call, and its connection with it.
func TestExecInPod_AnErrorAnswerClosesItsConnection(t *testing.T) {
	var opened, closed atomic.Int32
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("{}"))
	}))
	api.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			opened.Add(1)
		case http.StateClosed:
			closed.Add(1)
		}
	}
	api.Start()
	defer api.Close()
	client, err := NewClientWith(ClientOptions{APIURL: api.URL, BearerToken: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := client.ExecInPod(context.Background(), "ns", "pod", "c", []string{"true"}); err == nil {
			t.Fatal("exec succeeded against a 403")
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for closed.Load() < opened.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("%d connections opened, %d closed", opened.Load(), closed.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A proxy that takes the connection and never answers CONNECT. The proxy is given
// directly: http.ProxyFromEnvironment reads the environment once per process.
func TestExecThroughASilentProxy_EndsAtItsDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			defer conn.Close()
		}
	}()
	defer listener.Close()
	proxyURL := &url.URL{Scheme: "http", Host: listener.Addr().String()}
	target := &url.URL{Scheme: "http", Host: "kube.invalid:6443", Path: "/exec"}
	executor, err := newCancellableExecutor(&rest.Config{Host: "http://kube.invalid:6443", Proxy: http.ProxyURL(proxyURL)}, target)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: io.Discard}) }()
	select {
	case err := <-returned:
		// client-go flattens the cause into its message.
		if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
			t.Errorf("err = %v, want the deadline", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("exec through a silent proxy outlived its deadline")
	}
	if accepted.Load() == 0 {
		t.Fatal("the proxy was never used, so this proved nothing")
	}
}

// The dial left waiting on a proxy closes what it gets once nobody wants it.
func TestExecThroughALateProxy_ClosesTheConnectionItNoLongerWants(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		<-ctx.Done()
		time.Sleep(100 * time.Millisecond)
		if _, err := conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n")); err != nil {
			closed <- err
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = io.Copy(io.Discard, conn)
		closed <- err
	}()
	proxyURL := &url.URL{Scheme: "http", Host: listener.Addr().String()}
	target := &url.URL{Scheme: "http", Host: "kube.invalid:6443", Path: "/exec"}
	executor, err := newCancellableExecutor(&rest.Config{Host: "http://kube.invalid:6443", Proxy: http.ProxyURL(proxyURL)}, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: io.Discard}); err == nil {
		t.Fatal("exec succeeded")
	}
	if err := <-closed; err != nil {
		t.Fatalf("the proxy's connection stayed open: %v", err)
	}
}
