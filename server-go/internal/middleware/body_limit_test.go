package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func echoBody() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusTeapot)
			return
		}
		_, _ = w.Write(body)
	})
}

// chunked hides the length, as a chunked upload does.
type chunked struct{ io.Reader }

func TestLimitBodyPassesABodyWithinTheLimit(t *testing.T) {
	w := httptest.NewRecorder()
	LimitBody(8)(echoBody()).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader("12345678")))
	if w.Code != http.StatusOK || w.Body.String() != "12345678" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

// EXC-555: an oversized body is refused with 413 before the handler runs,
// whether or not it declared its length.
func TestLimitBodyRefusesAnOversizedBody(t *testing.T) {
	for name, req := range map[string]*http.Request{
		"declared": httptest.NewRequest("POST", "/", strings.NewReader("123456789")),
		"chunked":  httptest.NewRequest("POST", "/", chunked{strings.NewReader("123456789")}),
	} {
		if name == "chunked" {
			req.ContentLength = -1
		}
		w := httptest.NewRecorder()
		LimitBody(8)(echoBody()).ServeHTTP(w, req)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: got %d, want 413", name, w.Code)
		}
		if !strings.Contains(w.Body.String(), "8 bytes") {
			t.Errorf("%s: the refusal does not name the limit: %s", name, w.Body.String())
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestLimitBodyRefusesAnUnreadableBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/", failingReader{})
	req.ContentLength = -1
	w := httptest.NewRecorder()
	LimitBody(8)(echoBody()).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
}
