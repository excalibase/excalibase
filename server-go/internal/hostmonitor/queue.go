package hostmonitor

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

const (
	// queueDepth notices wait for the sender; more are dropped and logged.
	queueDepth = 64
	// noticeCooldown is how long one alert stays quiet after its raise was sent.
	noticeCooldown = 15 * time.Minute
	// noticesPerMinute bounds what the whole host sends in one minute.
	noticesPerMinute = 20
)

type queued struct {
	settings domain.MonitoringAlertSettings
	change   service.AlertChange
}

// Queue sends notices on its own goroutine, so a slow mail relay or webhook
// never holds the minute's sample, and keeps a flapping alert from flooding.
type Queue struct {
	inner   AlertNotifier
	pending chan queued
	done    chan struct{}
	now     func() time.Time

	mu             sync.Mutex
	raisedAt       map[string]time.Time
	unsent         map[string]bool
	minute         time.Time
	sentThisMinute int
}

func NewQueue(inner AlertNotifier) *Queue {
	queue := &Queue{inner: inner, pending: make(chan queued, queueDepth), done: make(chan struct{}), now: time.Now,
		raisedAt: map[string]time.Time{}, unsent: map[string]bool{}}
	go queue.run()
	return queue
}

// Notify admits the change or drops it; it never waits for a send.
func (q *Queue) Notify(_ context.Context, settings domain.MonitoringAlertSettings, change service.AlertChange) {
	if !q.admit(change) {
		return
	}
	select {
	case q.pending <- queued{settings: settings, change: change}:
	default:
		log.Printf("host monitor: notice queue full; a notice was dropped")
	}
}

// Close stops the sender once the queued notices are sent.
func (q *Queue) Close() {
	close(q.pending)
	<-q.done
}

func (q *Queue) run() {
	defer close(q.done)
	for item := range q.pending {
		q.inner.Notify(context.Background(), item.settings, item.change)
	}
}

// admit applies the cooldown per alert and the cap per minute.
func (q *Queue) admit(change service.AlertChange) bool {
	alert := change.Raised
	if alert == nil {
		alert = change.Resolved
	}
	key := alert.ProjectID + "\x00" + alert.Metric
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	if change.Raised != nil {
		if last, sent := q.raisedAt[key]; sent && now.Sub(last) < noticeCooldown {
			q.unsent[key] = true
			return false
		}
	} else if q.unsent[key] {
		// Its raise was never sent; its resolve is not news.
		delete(q.unsent, key)
		return false
	}
	if minute := now.Truncate(time.Minute); !minute.Equal(q.minute) {
		q.minute, q.sentThisMinute = minute, 0
	}
	if q.sentThisMinute >= noticesPerMinute {
		if change.Raised != nil {
			q.unsent[key] = true
		}
		return false
	}
	q.sentThisMinute++
	if change.Raised != nil {
		q.raisedAt[key] = now
		delete(q.unsent, key)
	}
	return true
}
