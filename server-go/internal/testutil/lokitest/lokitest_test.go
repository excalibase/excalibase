package lokitest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// recorder stands in for a test so a failure can be asserted on.
type recorder struct {
	testing.TB
	failure string
	logs    []string
}

func (r *recorder) Helper() {}

func (r *recorder) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) { r.failure = fmt.Sprintf(format, args...) }

func lokiAnswering(t *testing.T, answer func(call int64) (int, string)) (string, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/push" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		status, body := answer(calls.Add(1))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL, calls
}

func TestWaitAcceptsPush_RetriesAServerErrorAndLogsWhatLokiSaid(t *testing.T) {
	url, calls := lokiAnswering(t, func(call int64) (int, string) {
		if call < 3 {
			return http.StatusInternalServerError, "empty ring"
		}
		return http.StatusNoContent, ""
	})
	rec := &recorder{TB: t}
	WaitAcceptsPush(rec, url, 10*time.Second)
	if rec.failure != "" || calls.Load() != 3 {
		t.Fatalf("failure %q after %d pushes", rec.failure, calls.Load())
	}
	if len(rec.logs) != 2 || !strings.Contains(rec.logs[0], "500") || !strings.Contains(rec.logs[0], "empty ring") {
		t.Errorf("logs = %q", rec.logs)
	}
}

func TestWaitAcceptsPush_FailsAtOnceOnAnAnswerRetryingCannotFix(t *testing.T) {
	url, calls := lokiAnswering(t, func(int64) (int, string) { return http.StatusBadRequest, "entry too far behind" })
	rec := &recorder{TB: t}
	WaitAcceptsPush(rec, url, 10*time.Second)
	if calls.Load() != 1 || !strings.Contains(rec.failure, "400") || !strings.Contains(rec.failure, "entry too far behind") {
		t.Fatalf("failure %q after %d pushes", rec.failure, calls.Load())
	}
}

func TestWaitAcceptsPush_FailsWithLokisLastAnswerAtTheDeadline(t *testing.T) {
	url, _ := lokiAnswering(t, func(int64) (int, string) { return http.StatusInternalServerError, "at least 1 live replicas required" })
	rec := &recorder{TB: t}
	start := time.Now()
	WaitAcceptsPush(rec, url, time.Second)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %s past a 1s deadline", elapsed)
	}
	if !strings.Contains(rec.failure, "500") || !strings.Contains(rec.failure, "at least 1 live replicas required") {
		t.Fatalf("failure = %q", rec.failure)
	}
}

func TestBody_IsBoundedAndTrimmed(t *testing.T) {
	resp := &http.Response{Body: nopCloser{strings.NewReader("  " + strings.Repeat("x", 10_000) + "\n")}}
	if got := Body(resp); got == "" || len(got) > maxBody || strings.TrimSpace(got) != got {
		t.Errorf("len %d", len(got))
	}
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func TestWaitAcceptsPush_FailsAtOnceWhenLokiIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	rec := &recorder{TB: t}
	start := time.Now()
	WaitAcceptsPush(rec, url, 10*time.Second)
	if rec.failure == "" || time.Since(start) > 5*time.Second {
		t.Fatalf("failure %q after %s", rec.failure, time.Since(start))
	}
}

type fakeLogs struct {
	text string
	err  error
}

func (f fakeLogs) Logs(context.Context) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.text)), nil
}

func TestTail_KeepsTheEndOfLokisOwnLog(t *testing.T) {
	text := "level=warn msg=\"disk usage exceeded threshold, throttling writes\"\n" + strings.Repeat("noise\n", 5000)
	got := Tail(fakeLogs{text: text})
	if len(got) > maxLog || !strings.HasSuffix(got, "noise") {
		t.Fatalf("len %d, ends %q", len(got), got[len(got)-10:])
	}
	short := "level=warn msg=\"disk usage exceeded threshold, throttling writes\""
	if got := Tail(fakeLogs{text: short}); got != short {
		t.Errorf("short log = %q", got)
	}
}

func TestTail_SaysSoWhenLokisLogCannotBeRead(t *testing.T) {
	got := Tail(fakeLogs{err: errors.New("container is gone")})
	if !strings.Contains(got, "container is gone") {
		t.Errorf("got %q", got)
	}
}
