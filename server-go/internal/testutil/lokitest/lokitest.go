// Package lokitest holds what tests against a real Loki container share.
package lokitest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// maxBody bounds how much of Loki's answer a failure quotes.
const maxBody = 2 << 10

// AcceptsWithin bounds the wait, after /ready, for Loki to store a push.
const AcceptsWithin = 60 * time.Second

const retryEvery = 500 * time.Millisecond

// probeLabels name a stream no test selects, labels or deletes.
var probeLabels = map[string]string{"lokitest": "probe"}

// Body is the start of Loki's answer, for a failure message.
func Body(resp *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "(reading Loki's answer: " + err.Error() + ")"
	}
	return strings.TrimSpace(string(raw))
}

// WaitAcceptsPush pushes a probe line until Loki stores it: /ready can pass
// while a push still answers 5xx. Anything else, or the deadline, fails t.
func WaitAcceptsPush(t testing.TB, lokiURL string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		status, body, err := pushProbe(lokiURL)
		switch {
		case err != nil:
			t.Fatalf("probe push: %v", err)
			return
		case status == http.StatusNoContent:
			return
		case status < http.StatusInternalServerError:
			t.Fatalf("Loki refused the probe push: %d %q", status, body)
			return
		case time.Now().After(deadline):
			t.Fatalf("Loki took no push within %s; last answer %d %q", within, status, body)
			return
		}
		t.Logf("Loki not taking pushes yet: %d %q", status, body)
		time.Sleep(retryEvery)
	}
}

func pushProbe(lokiURL string) (int, string, error) {
	line := [2]string{strconv.FormatInt(time.Now().UnixNano(), 10), "probe"}
	payload, err := json.Marshal(map[string]any{"streams": []any{map[string]any{"stream": probeLabels, "values": [][2]string{line}}}})
	if err != nil {
		return 0, "", err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(lokiURL+"/loki/api/v1/push", "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	return resp.StatusCode, Body(resp), nil
}
