package hostlogs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// syncPusher records pushes across follower goroutines.
type syncPusher struct {
	mu    sync.Mutex
	lines []string
}

func (p *syncPusher) Push(_ context.Context, streams []Stream) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, stream := range streams {
		for _, entry := range stream.Entries {
			p.lines = append(p.lines, stream.Labels.ProjectID+":"+entry.Line)
		}
	}
	return nil
}

func (p *syncPusher) got() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lines...)
}

// Run ships each project's lines under its own labels, through the real engine client.
func TestRunShipsThroughTheEngineClient(t *testing.T) {
	frame := func(text string) string {
		size := len(text)
		return string([]byte{1, 0, 0, 0, byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)}) + text
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"db-a","Names":["/` + provisioner.DatabaseContainerName("proj-a") + `"],"State":"running","Labels":{"excalibase.managed":"true"}},
				{"Id":"db-b","Names":["/` + provisioner.DatabaseContainerName("proj-b") + `"],"State":"running","Labels":{"excalibase.managed":"true"}}]`))
		case "/containers/db-a/logs":
			_, _ = w.Write([]byte(frame(now.Format(time.RFC3339Nano) + " LOG: from a\n")))
		case "/containers/db-b/logs":
			_, _ = w.Write([]byte(frame(now.Format(time.RFC3339Nano) + " LOG: from b\n")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := enginestats.New("tcp://" + strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	pusher := &syncPusher{}
	shipper := New(Deps{
		Engine: EngineFrom(client), Cursors: &fakeCursors{}, Pusher: pusher, Flags: features.NewStatic(),
		Instances:  instances{dockerProject("proj-a", domain.Free, "db-a"), dockerProject("proj-b", domain.Free, "db-b")},
		DailyBytes: 1 << 20, Now: func() time.Time { return now.Add(time.Minute) }, RetryDelay: time.Hour, ScanEvery: time.Hour,
	})
	shipper.deps.Retention.Default = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { shipper.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(pusher.got()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	got := strings.Join(pusher.got(), ",")
	if !strings.Contains(got, "proj-a:LOG: from a") || !strings.Contains(got, "proj-b:LOG: from b") || strings.Contains(got, "proj-a:LOG: from b") {
		t.Errorf("pushed %s", got)
	}
	if shipper.following() != 0 {
		t.Errorf("following %d after Run returned", shipper.following())
	}
}

// A follow that fails is reopened from its cursor after the retry delay.
func TestFollowReopensAfterAFailure(t *testing.T) {
	engine := &fakeEngine{followErr: errors.New("engine follow: answered 502")}
	shipper := newTestShipper(engine, &fakeCursors{}, &fakePusher{}, features.NewStatic())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	shipper.wg.Add(1)
	shipper.follow(ctx, Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour}, 0)
	if _, asked := engine.since["db-a"]; !asked {
		t.Error("never followed")
	}
}

type failingInstances struct{}

func (failingInstances) FindAll() ([]*domain.DatabaseInstance, error) {
	return nil, errors.New("db down")
}

func TestScanReportsWhatItCouldNotRead(t *testing.T) {
	shipper := newTestShipper(&fakeEngine{}, &fakeCursors{}, &fakePusher{}, features.NewStatic())
	shipper.deps.Instances = failingInstances{}
	if err := shipper.Scan(context.Background(), false); err == nil {
		t.Error("no error with the projects unreadable")
	}
	shipper = newTestShipper(&fakeEngine{}, &fakeCursors{failed: errors.New("db down")}, &fakePusher{}, features.NewStatic())
	if err := shipper.Scan(context.Background(), false); err == nil {
		t.Error("no error with the cursors unreadable")
	}
}

// endlessLines never ends on its own; Close ends it, as closing a follow does.
type endlessLines struct {
	closed chan struct{}
	once   sync.Once
	n      int
}

func (e *endlessLines) Next() (enginestats.LogLine, error) {
	select {
	case <-e.closed:
		return enginestats.LogLine{}, errors.New("closed")
	default:
	}
	e.n++
	return enginestats.LogLine{At: now.Add(time.Duration(e.n) * time.Millisecond), Text: "x"}, nil
}

func (e *endlessLines) Close() error {
	e.once.Do(func() { close(e.closed) })
	return nil
}

type endlessEngine struct{ *fakeEngine }

func (endlessEngine) FollowLogs(context.Context, string, time.Time) (Lines, error) {
	return &endlessLines{closed: make(chan struct{})}, nil
}

type failingSave struct{ *fakeCursors }

func (*failingSave) SaveHostLogCursor(context.Context, string, string, int64) error {
	return errors.New("db down")
}

// A follow that gives up (here: its cursor cannot be saved) leaves no reader behind.
func TestShipStreamLeavesNoReaderBehind(t *testing.T) {
	before := runtime.NumGoroutine()
	shipper := New(Deps{Engine: endlessEngine{&fakeEngine{}}, Cursors: &failingSave{&fakeCursors{}}, Pusher: &fakePusher{}, Instances: instances{},
		Flags: features.NewStatic(), DailyBytes: 1 << 30, Now: func() time.Time { return now }, RetryDelay: time.Millisecond})
	target := Target{ID: "db-a", Labels: Labels{ProjectID: "proj-a", Component: "database", Container: "c"}, Retention: time.Hour}
	for i := 0; i < 5; i++ {
		if _, err := shipper.shipStream(context.Background(), target, 0); err == nil {
			t.Fatal("a failed cursor save reported success")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("%d goroutines before, %d after", before, after)
	}
}
