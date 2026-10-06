package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A log backend that cannot be reached is reported without its address: the
// answer reaches Studio and coding tools, which must not learn internal hosts.
func TestLokiFailuresNameNoInternalAddress(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>" + failing.URL))
	}))
	defer garbled.Close()

	for name, lokiURL := range map[string]string{"unreachable": closed.URL, "erroring": failing.URL, "garbled": garbled.URL, "malformed": "http://%zz"} {
		t.Run(name, func(t *testing.T) {
			svc := &ProvisioningService{lokiURL: lokiURL}
			_, err := svc.getLogsFromLoki(context.Background(), "ns", "proj-a", 10)
			if !errors.Is(err, ErrLogsUnavailable) {
				t.Fatalf("err = %v, want ErrLogsUnavailable", err)
			}
			if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "http") {
				t.Fatalf("the error names the backend: %v", err)
			}
		})
	}
}
