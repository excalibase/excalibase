package hostmonitor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// slowNotifier records what reached the channels, and can hold each send.
type slowNotifier struct {
	mu    sync.Mutex
	sent  []service.AlertChange
	block chan struct{}
}

func (s *slowNotifier) Notify(_ context.Context, _ domain.MonitoringAlertSettings, change service.AlertChange) {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, change)
}

func (s *slowNotifier) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func raised(project, metric string) service.AlertChange {
	return service.AlertChange{Raised: &domain.Alert{ID: metric, ProjectID: project, Metric: metric}}
}

func resolved(project, metric string) service.AlertChange {
	return service.AlertChange{Resolved: &domain.Alert{ID: metric, ProjectID: project, Metric: metric, Resolved: true}}
}

func waitFor(t *testing.T, want int, inner *slowNotifier) {
	t.Helper()
	for range 200 {
		if inner.count() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%d notices sent, want %d", inner.count(), want)
}

func TestTheQueueNeverHoldsTheSample(t *testing.T) {
	inner := &slowNotifier{block: make(chan struct{})}
	queue := NewQueue(inner)
	defer queue.Close()
	done := make(chan struct{})
	go func() {
		for i := range queueDepth + 10 {
			queue.Notify(context.Background(), domain.MonitoringAlertSettings{}, raised("p1", string(rune('a'+i%26))+time.Now().String()))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify waited for a send")
	}
	close(inner.block)
}

// A flapping container sends its raise, then nothing for the cooldown; its
// resolve is sent only when its raise was.
func TestAFlappingAlertIsSentOncePerCooldown(t *testing.T) {
	inner := &slowNotifier{}
	queue := NewQueue(inner)
	defer queue.Close()
	now := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }
	settings := domain.MonitoringAlertSettings{}
	queue.Notify(context.Background(), settings, raised("p1", "container.down:app"))
	queue.Notify(context.Background(), settings, resolved("p1", "container.down:app"))
	for range 5 {
		now = now.Add(time.Minute)
		queue.Notify(context.Background(), settings, raised("p1", "container.down:app"))
		queue.Notify(context.Background(), settings, resolved("p1", "container.down:app"))
	}
	waitFor(t, 2, inner)
	now = now.Add(noticeCooldown)
	queue.Notify(context.Background(), settings, raised("p1", "container.down:app"))
	waitFor(t, 3, inner)
	queue.Notify(context.Background(), settings, raised("p2", "container.down:app"))
	waitFor(t, 4, inner)
}

func TestNoticesPerMinuteAreCapped(t *testing.T) {
	inner := &slowNotifier{}
	queue := NewQueue(inner)
	defer queue.Close()
	now := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }
	for i := range noticesPerMinute + 5 {
		queue.Notify(context.Background(), domain.MonitoringAlertSettings{}, raised("p1", "m"+string(rune('a'+i))))
	}
	waitFor(t, noticesPerMinute, inner)
	now = now.Add(time.Minute)
	queue.Notify(context.Background(), domain.MonitoringAlertSettings{}, raised("p1", "later"))
	waitFor(t, noticesPerMinute+1, inner)
}
