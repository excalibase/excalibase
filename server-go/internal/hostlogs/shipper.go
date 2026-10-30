// Package hostlogs ships a single host's project container output to the
// bundled Loki (EXC-672), so the project log search reads stored lines with
// the same query code as Kubernetes. Provisioning is the collector: it
// follows each container's log through the engine proxy, which it already
// uses, and no other process gets the engine.
package hostlogs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/hostmonitor"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
)

const (
	defaultScanEvery  = 30 * time.Second
	defaultRetryDelay = 5 * time.Second
	defaultFlushEvery = 2 * time.Second
	// batchLines and batchBytes bound one push, well under Loki's per-stream burst.
	batchLines = 500
	batchBytes = 1 << 20
	// queuedLines is how many cut lines wait between the engine and a batch.
	queuedLines = 64
	// maxRetryDelay caps the backoff of a refused or failed push.
	maxRetryDelay = time.Minute
	// finalFlush is how long a stopping follow may take to store what it read.
	finalFlush = 5 * time.Second
	// maxClockSkew is how far ahead of this host an engine stamp is believed.
	maxClockSkew = time.Minute
	// maxFollowers bounds the open follows, one per running project container.
	maxFollowers = 256
	pruneEvery   = time.Hour
)

// Lines is a followed container log.
type Lines interface {
	Next() (enginestats.LogLine, error)
	Close() error
}

// Engine is the engine proxy's container list and log follow.
type Engine interface {
	Managed(ctx context.Context) ([]enginestats.Container, error)
	FollowLogs(ctx context.Context, ref string, since time.Time) (Lines, error)
}

// Cursors keeps, per container, the newest line Loki has stored.
type Cursors interface {
	HostLogCursors(ctx context.Context) (map[string]int64, error)
	SaveHostLogCursor(ctx context.Context, containerID, projectID string, shippedUntilNS int64) error
	PruneHostLogCursors(ctx context.Context, keep []string) error
}

// Pusher stores streams of lines.
type Pusher interface {
	Push(ctx context.Context, streams []Stream) error
}

// Instances lists projects, to attribute containers and read their plans.
type Instances interface {
	FindAll() ([]*domain.DatabaseInstance, error)
}

// DiskState says whether the host's disk is too full to store more lines.
type DiskState interface {
	Paused() bool
}

// Deps is what a Shipper is built from.
type Deps struct {
	Engine    Engine
	Cursors   Cursors
	Pusher    Pusher
	Instances Instances
	// Budgets keeps each project's day of stored text across restarts; nil counts in memory only.
	Budgets BudgetStore
	// Disk pauses every project's shipping while the host disk is nearly full; nil never pauses.
	Disk DiskState
	// Flags turns the plan label on with features.LogRetention.
	Flags     features.Flags
	Retention config.LogRetention
	// DailyBytes is each project's stored log text per UTC day, unless
	// DailyBytesByPlan names the project's plan.
	DailyBytes       int64
	DailyBytesByPlan map[domain.TierType]int64
	Now              func() time.Time
	ScanEvery        time.Duration
	RetryDelay       time.Duration
	FlushEvery       time.Duration
}

// Target is one container to follow and the labels its lines carry.
type Target struct {
	ID        string
	Labels    Labels
	Retention time.Duration
	// DailyBytes is the project's plan's budget; zero takes Deps.DailyBytes.
	DailyBytes int64
}

// Shipper follows every running project container.
type Shipper struct {
	deps   Deps
	budget *budget

	mu        sync.Mutex
	followers map[string]*follower
	stopping  map[string]*follower
	stopped   bool
	wg        sync.WaitGroup
}

// follower is one container's running follow.
type follower struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// New builds a Shipper; zero durations take their defaults.
func New(deps Deps) *Shipper {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.ScanEvery <= 0 {
		deps.ScanEvery = defaultScanEvery
	}
	if deps.RetryDelay <= 0 {
		deps.RetryDelay = defaultRetryDelay
	}
	if deps.FlushEvery <= 0 {
		deps.FlushEvery = defaultFlushEvery
	}
	return &Shipper{deps: deps, budget: newBudget(deps.Budgets), followers: map[string]*follower{}, stopping: map[string]*follower{}}
}

// Run scans now and every ScanEvery until ctx ends, then stops every follow.
func (s *Shipper) Run(ctx context.Context) {
	defer s.Stop()
	ticker := time.NewTicker(s.deps.ScanEvery)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		prune := s.deps.Now().Sub(lastPrune) >= pruneEvery
		if err := s.Scan(ctx, prune); err != nil {
			log.Printf("host logs: scan: %v", err)
		} else if prune {
			lastPrune = s.deps.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Scan follows each running project container not yet followed, stops the
// follows of containers that went, and with prune forgets the cursors of
// containers the engine no longer has.
func (s *Shipper) Scan(ctx context.Context, prune bool) error {
	if s.diskLow() {
		// Nothing is read while paused: the engine keeps the lines, the cursors stay.
		s.reconcile(ctx, nil, nil, nil)
		return nil
	}
	targets, seen, err := s.targets(ctx)
	if err != nil {
		return err
	}
	cursors, err := s.deps.Cursors.HostLogCursors(ctx)
	if err != nil {
		return fmt.Errorf("cursors: %w", err)
	}
	s.reconcile(ctx, targets, seen, cursors)
	// An empty answer is more likely an engine hiccup than no containers at all.
	if !prune {
		return nil
	}
	var budgetErr, cursorErr error
	if s.deps.Budgets != nil {
		if err := s.deps.Budgets.PruneHostLogBudget(ctx, s.deps.Now().UTC().Add(-48*time.Hour).Format(time.DateOnly)); err != nil {
			budgetErr = fmt.Errorf("prune budgets: %w", err)
		}
	}
	if len(seen) > 0 {
		cursorErr = s.deps.Cursors.PruneHostLogCursors(ctx, seen)
	}
	return errors.Join(budgetErr, cursorErr)
}

func (s *Shipper) diskLow() bool {
	return s.deps.Disk != nil && s.deps.Disk.Paused()
}

func (s *Shipper) reconcile(ctx context.Context, targets []Target, seen []string, cursors map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	present := make(map[string]bool, len(seen))
	for _, id := range seen {
		present[id] = true
	}
	wanted := make(map[string]bool, len(targets))
	for _, target := range targets {
		wanted[target.ID] = true
		if _, running := s.followers[target.ID]; running || s.stillStopping(target.ID) {
			continue
		}
		if len(s.followers) >= maxFollowers {
			log.Printf("host logs: %d containers followed already; %s waits", maxFollowers, target.Labels.Container)
			continue
		}
		followCtx, cancel := context.WithCancel(ctx)
		run := &follower{cancel: cancel, done: make(chan struct{})}
		s.followers[target.ID] = run
		s.wg.Add(1)
		go func() {
			defer close(run.done)
			s.follow(followCtx, target, cursors[target.ID])
			s.forget(target.ID, run)
		}()
	}
	for id, run := range s.followers {
		// A stopped container ends its follow itself, after its last lines are stored.
		if !wanted[id] && !present[id] {
			run.cancel()
			delete(s.followers, id)
			s.stopping[id] = run
		}
	}
}

// forget drops a follow that ended by itself.
func (s *Shipper) forget(id string, run *follower) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.followers[id] == run {
		delete(s.followers, id)
	}
}

// stillStopping is a follow of id that was told to stop and has not yet:
// a new one waits for it, so the two never push the same lines.
func (s *Shipper) stillStopping(id string) bool {
	run, ok := s.stopping[id]
	if !ok {
		return false
	}
	select {
	case <-run.done:
		delete(s.stopping, id)
		return false
	default:
		return true
	}
}

// Stop ends every follow and waits for them; nothing is followed after it.
func (s *Shipper) Stop() {
	s.mu.Lock()
	s.stopped = true
	for id, run := range s.followers {
		run.cancel()
		delete(s.followers, id)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Shipper) following() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.followers)
}

// targets are the running project containers, labelled; seen is every
// managed container's id, whose cursors are kept.
func (s *Shipper) targets(ctx context.Context) ([]Target, []string, error) {
	instances, err := s.deps.Instances.FindAll()
	if err != nil {
		return nil, nil, fmt.Errorf("projects: %w", err)
	}
	containers, err := s.deps.Engine.Managed(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("containers: %w", err)
	}
	tiers := make(map[string]domain.TierType, len(instances))
	for _, inst := range instances {
		tiers[inst.ProjectID] = inst.Tier
	}
	perPlan := features.Enabled(ctx, s.deps.Flags, features.LogRetention)
	attribute := hostmonitor.Attribution(instances)
	var targets []Target
	seen := make([]string, 0, len(containers))
	for _, container := range containers {
		seen = append(seen, container.ID)
		projectID, component := attribute(container)
		// Only what the project log search reads is stored (EXC-672).
		if projectID == "" || component != domain.ContainerDatabase || container.State != "running" {
			continue
		}
		target := Target{ID: container.ID, Labels: Labels{ProjectID: projectID, Component: component, Container: container.Name}}
		target.Retention = s.deps.Retention.Default
		target.DailyBytes = s.dailyBytes(tiers[projectID])
		if tier, ok := tiers[projectID]; ok && perPlan && domain.IsValidTier(tier) {
			target.Labels.Tier = strings.ToLower(string(tier))
			target.Retention = s.deps.Retention.For(tier)
		}
		if !target.Labels.valid() {
			continue
		}
		targets = append(targets, target)
	}
	return targets, seen, nil
}

// dailyBytes is the plan's budget, or the default for a plan not named.
func (s *Shipper) dailyBytes(tier domain.TierType) int64 {
	if limit, ok := s.deps.DailyBytesByPlan[tier]; ok && limit > 0 {
		return limit
	}
	return s.deps.DailyBytes
}

// follow ships the container's log until ctx ends, reopening it after the
// engine ends the stream (a restart) or a failure.
func (s *Shipper) follow(ctx context.Context, target Target, cursor int64) {
	defer s.wg.Done()
	for ctx.Err() == nil {
		next, err := s.shipStream(ctx, target, cursor)
		cursor = max(cursor, next)
		if err == nil {
			// The engine ended the stream: the container stopped or went.
			return
		}
		if ctx.Err() == nil {
			log.Printf("host logs: %s: %v", target.Labels.Container, err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(s.deps.RetryDelay):
		}
	}
}

// shipStream follows the log once, from the cursor or no further back than
// the retention, pushing batches and saving the cursor after each is stored.
// It answers the newest stored line's time.
func (s *Shipper) shipStream(ctx context.Context, target Target, cursor int64) (int64, error) {
	if s.diskLow() {
		return cursor, errDiskLow
	}
	since := s.deps.Now().Add(-target.Retention)
	if cursor > since.UnixNano() {
		since = time.Unix(0, cursor)
	}
	// The reader ends with this follow, whichever way it returns.
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	stream, err := s.deps.Engine.FollowLogs(streamCtx, target.ID, since)
	if err != nil {
		return cursor, err
	}
	defer stream.Close()
	lines := make(chan enginestats.LogLine, queuedLines)
	done := make(chan error, 1)
	go readLines(streamCtx, stream, lines, done)

	batch := newBatch(target, since.UnixNano(), cursor)
	// Whatever this follow read but did not store is read and charged again later.
	defer s.unstored(batch)
	ticker := time.NewTicker(s.deps.FlushEvery)
	defer ticker.Stop()
	for {
		select {
		case line, open := <-lines:
			if !open {
				err := <-done
				if flushErr := s.flush(ctx, batch); flushErr != nil {
					return batch.stored, flushErr
				}
				return batch.stored, err
			}
			s.add(ctx, batch, line)
			if len(batch.entries) >= batchLines || batch.bytes >= batchBytes {
				if err := s.flush(ctx, batch); err != nil {
					return batch.stored, err
				}
			}
		case <-ticker.C:
			if err := s.flush(ctx, batch); err != nil {
				return batch.stored, err
			}
		case <-ctx.Done():
			// Told to stop: store what was read, briefly, rather than read it again.
			finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalFlush)
			_ = s.flush(finalCtx, batch)
			cancel()
			return batch.stored, ctx.Err()
		}
	}
}

func readLines(ctx context.Context, stream Lines, out chan<- enginestats.LogLine, done chan<- error) {
	defer close(out)
	for {
		line, err := stream.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			done <- err
			return
		}
		line.Text = logdrain.TruncateLine(line.Text)
		select {
		case out <- line:
		case <-ctx.Done():
			done <- ctx.Err()
			return
		}
	}
}

// add keeps a line newer than the cursor and within the day's budget; a
// skipped line still moves the cursor.
func (s *Shipper) add(ctx context.Context, b *batch, line enginestats.LogLine) {
	// A stamp from the future would move the cursor past every real line to come.
	at := min(line.At.UnixNano(), s.deps.Now().Add(maxClockSkew).UnixNano())
	if at <= b.floor {
		return
	}
	b.seen = max(b.seen, at)
	text := line.Text
	limit := b.target.DailyBytes
	if limit <= 0 {
		limit = s.deps.DailyBytes
	}
	keep, notice := s.budget.take(ctx, b.target.Labels.ProjectID, limit, line.At, int64(len(text)))
	day := line.At.UTC().Format(time.DateOnly)
	switch {
	case keep:
		b.entries = append(b.entries, Entry{At: at, Line: text})
		b.bytes += len(text)
		b.charge(day).bytes += int64(len(text))
	case notice:
		b.entries = append(b.entries, Entry{At: at, Line: fmt.Sprintf(
			"excalibase: this project's daily log budget (%d MiB) is used; its lines are not stored until 00:00 UTC", limit>>20)})
		b.charge(day).noticed = true
	}
}

// flush pushes the batch until Loki stores it, then saves the cursor.
func (s *Shipper) flush(ctx context.Context, b *batch) error {
	if len(b.entries) == 0 && b.seen <= b.stored {
		return nil
	}
	if s.diskLow() {
		return errDiskLow
	}
	if len(b.entries) > 0 {
		stream := []Stream{{Labels: b.target.Labels, Entries: b.entries}}
		for delay := s.deps.RetryDelay; ; delay = min(2*delay, maxRetryDelay) {
			err := s.deps.Pusher.Push(ctx, stream)
			if err == nil {
				break
			}
			log.Printf("host logs: push %s: %v", b.target.Labels.Container, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	if err := s.budget.record(ctx, b.target.Labels.ProjectID, b.charges); err != nil {
		// The in-memory count still holds; only a restart before the next write loses it.
		log.Printf("host logs: budget of %s: %v", b.target.Labels.ProjectID, err)
	}
	clear(b.charges)
	if err := s.deps.Cursors.SaveHostLogCursor(ctx, b.target.ID, b.target.Labels.ProjectID, b.seen); err != nil {
		return fmt.Errorf("save cursor: %w", err)
	}
	b.stored = b.seen
	b.entries = b.entries[:0]
	b.bytes = 0
	return nil
}

// batch is the lines read since the last push.
type batch struct {
	target  Target
	entries []Entry
	bytes   int
	// floor is the newest line stored before this follow, or the retention's
	// edge; a reopened follow reads its last second again and skips up to it.
	floor int64
	// seen is the newest line read; stored the newest Loki has.
	seen, stored int64
	// charges is what the entries took from each day's budget.
	charges map[string]*charge
}

func newBatch(target Target, sinceNS, cursor int64) *batch {
	floor := max(cursor, sinceNS-1)
	return &batch{target: target, floor: floor, seen: cursor, stored: cursor, charges: map[string]*charge{}}
}

// unstored refunds what the batch took from the budget and was not stored.
func (s *Shipper) unstored(b *batch) {
	if len(b.charges) > 0 {
		s.budget.refund(b.target.Labels.ProjectID, b.charges)
		clear(b.charges)
	}
}

func (b *batch) charge(day string) *charge {
	taken := b.charges[day]
	if taken == nil {
		taken = &charge{}
		b.charges[day] = taken
	}
	return taken
}
