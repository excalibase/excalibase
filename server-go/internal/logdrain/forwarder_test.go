package logdrain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePlane struct {
	mu      sync.Mutex
	batches [][]Batch
	pullErr error
	pulls   int
	maxes   []int
	acks    []Ack
}

func (f *fakePlane) Pull(_ context.Context, max int) ([]Batch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls++
	f.maxes = append(f.maxes, max)
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	next := f.batches[0]
	f.batches = f.batches[1:]
	return next, nil
}

func (f *fakePlane) Ack(_ context.Context, ack Ack) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, ack)
	return nil
}

type fakeSender struct {
	mu      sync.Mutex
	failFor map[string]error
	sent    []string
}

func (f *fakeSender) Send(_ context.Context, batch Batch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, batch.DrainID)
	return f.failFor[batch.DrainID]
}

func TestForwarder_AcksEachBatchWithItsOutcome(t *testing.T) {
	plane := &fakePlane{batches: [][]Batch{{
		{DrainID: "ld_ok", From: 10, To: 20, Lines: sampleLines},
		{DrainID: "ld_bad", From: 5, To: 9, Lines: sampleLines[:1], Secret: testSecret},
	}}}
	sender := &fakeSender{failFor: map[string]error{"ld_bad": sendFailure("the destination answered 401")}}
	n, err := NewForwarder(plane, sender).RunOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}
	want := map[string]Ack{
		"ld_ok":  {DrainID: "ld_ok", From: 10, To: 20, Lines: 2},
		"ld_bad": {DrainID: "ld_bad", From: 5, To: 9, Lines: 1, Error: "the destination answered 401"},
	}
	if len(plane.acks) != 2 {
		t.Fatalf("acks = %+v", plane.acks)
	}
	for _, ack := range plane.acks {
		if ack != want[ack.DrainID] {
			t.Errorf("ack %+v", ack)
		}
	}
}

// A sender error that is not a SendError could carry anything; only a fixed
// message is reported back.
func TestForwarder_UnknownErrorsAreNotEchoed(t *testing.T) {
	plane := &fakePlane{batches: [][]Batch{{{DrainID: "ld_x", Secret: testSecret}}}}
	sender := &fakeSender{failFor: map[string]error{"ld_x": errors.New("Post https://x/?k=" + testSecret)}}
	if _, err := NewForwarder(plane, sender).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := plane.acks[0].Error; got == "" || strings.Contains(got, testSecret) || strings.Contains(got, "https://") {
		t.Errorf("ack error %q", got)
	}
}

// An oversized batch is refused, not sent: the bounds hold whatever arrives.
func TestForwarder_RefusesOversizedBatch(t *testing.T) {
	lines := make([]Line, MaxBatchLines+1)
	plane := &fakePlane{batches: [][]Batch{{{DrainID: "ld_big", Lines: lines}}}}
	sender := &fakeSender{}
	if _, err := NewForwarder(plane, sender).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 0 || plane.acks[0].Error == "" {
		t.Errorf("sent %v, ack %+v", sender.sent, plane.acks)
	}
}

// A control plane that keeps failing is retried with growing pauses, never
// in a hot loop.
func TestForwarder_BacksOffWhenThePlaneFails(t *testing.T) {
	plane := &fakePlane{pullErr: errors.New("connection refused")}
	forwarder := NewForwarder(plane, &fakeSender{})
	var waits []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	forwarder.sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if len(waits) == 8 {
			cancel()
			return false
		}
		return true
	}
	forwarder.Run(ctx)
	if len(waits) != 8 || waits[0] != idleWait {
		t.Fatalf("waits = %v", waits)
	}
	for i := 1; i < len(waits); i++ {
		if waits[i] < waits[i-1] || waits[i] > maxPlaneBackoff {
			t.Errorf("waits = %v", waits)
		}
	}
	if waits[len(waits)-1] != maxPlaneBackoff {
		t.Errorf("never reached the cap: %v", waits)
	}
}

func TestForwarder_PausesBetweenCycles(t *testing.T) {
	plane := &fakePlane{batches: [][]Batch{{{DrainID: "ld_1"}}}}
	forwarder := NewForwarder(plane, &fakeSender{})
	var waits []time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	forwarder.sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if len(waits) == 3 {
			cancel()
			return false
		}
		return true
	}
	forwarder.Run(ctx)
	for _, wait := range waits {
		if wait < idleWait {
			t.Errorf("waits = %v", waits)
		}
	}
}

func TestHTTPControlPlane_PullAndAck(t *testing.T) {
	var acked Ack
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(TokenHeader) != "fwd-token" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case PullPath:
			var asked PullRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil || asked.Max != 4 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(PullResponse{Batches: []Batch{{DrainID: "ld_1", From: 1761386400000000000, To: 1761386400000000001, Lines: sampleLines}}})
		case AckPath:
			_ = json.NewDecoder(r.Body).Decode(&acked)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	plane := NewHTTPControlPlane(server.URL, "fwd-token", server.Client())
	batches, err := plane.Pull(context.Background(), 4)
	if err != nil || len(batches) != 1 || batches[0].From != 1761386400000000000 || len(batches[0].Lines) != 2 {
		t.Fatalf("Pull = %+v, %v", batches, err)
	}
	if err := plane.Ack(context.Background(), Ack{DrainID: "ld_1", From: 1, To: 2, Lines: 2}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if acked.DrainID != "ld_1" || acked.To != 2 {
		t.Errorf("acked %+v", acked)
	}

	wrong := NewHTTPControlPlane(server.URL, "other", server.Client())
	if _, err := wrong.Pull(context.Background(), 4); err == nil {
		t.Error("a refused pull must be an error")
	}
}

// blockingSender holds one drain's send until released, as a slow destination would.
type blockingSender struct {
	release chan struct{}
	fast    chan string
}

func (b *blockingSender) Send(ctx context.Context, batch Batch) error {
	if batch.DrainID == "ld_slow" {
		select {
		case <-b.release:
		case <-ctx.Done():
		}
		return nil
	}
	b.fast <- batch.DrainID
	return nil
}

// A slow destination holds only its own send, never the other drains'.
func TestForwarder_SlowDestinationDoesNotBlockOthers(t *testing.T) {
	plane := &fakePlane{batches: [][]Batch{{{DrainID: "ld_slow"}, {DrainID: "ld_fast"}}}}
	sender := &blockingSender{release: make(chan struct{}), fast: make(chan string, 1)}
	done := make(chan struct{})
	go func() {
		_, _ = NewForwarder(plane, sender).RunOnce(context.Background())
		close(done)
	}()
	select {
	case id := <-sender.fast:
		if id != "ld_fast" {
			t.Errorf("sent %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fast drain waited for the slow one")
	}
	close(sender.release)
	<-done
	if len(plane.acks) != 2 {
		t.Errorf("acks %+v", plane.acks)
	}
}

// Only as many batches are asked for as there are free workers.
func TestForwarder_PullsForFreeWorkers(t *testing.T) {
	plane := &fakePlane{}
	if _, err := NewForwarder(plane, &fakeSender{}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(plane.maxes) != 1 || plane.maxes[0] != sendWorkers {
		t.Errorf("maxes %v", plane.maxes)
	}
}
