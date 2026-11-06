package k8s

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"
)

// The build's deadline is what ends a hung exec, since neither the client nor
// the role SQL's session has a timeout of its own: pin that the real client
// returns when its context ends, wherever the API server is holding it.
func TestExecInPod_ReturnsWhenItsContextEnds(t *testing.T) {
	servers := map[string]func(release <-chan struct{}) http.HandlerFunc{
		"before the upgrade is answered": func(release <-chan struct{}) http.HandlerFunc {
			return func(http.ResponseWriter, *http.Request) { <-release }
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
		})
	}
}
