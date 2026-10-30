package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
)

const (
	// logIngestDelay leaves lines still being shipped to Loki for the next read.
	logIngestDelay = 10 * time.Second
	// singleHostIngestDelay covers the single-host shipper's 30 s scan for new
	// containers, whose first lines reach Loki that much after they were written.
	singleHostIngestDelay = 45 * time.Second
	// logMaxLag is how far behind a drain may fall before it skips ahead.
	logMaxLag = 15 * time.Minute
	// logResumeWindow is how far back a drain that skipped ahead resumes.
	logResumeWindow = time.Minute
	// logIdlePoll is the pause after a read that found less than a full batch.
	logIdlePoll   = 10 * time.Second
	logStoreRetry = time.Minute
	// logDrainLease outlasts a send and its ack; an unacked batch is handed
	// out again only after it.
	logDrainLease      = time.Minute
	minLogDrainWait    = 30 * time.Second
	maxLogDrainBackoff = 30 * time.Minute
	// logPullTime is when a pull stops starting drains; logPullDeadline ends
	// its Loki reads, well inside the forwarder's 30 s wait.
	logPullTime     = 10 * time.Second
	logPullDeadline = 20 * time.Second
	// lineEntryOverhead covers the JSON fields around each message.
	lineEntryOverhead = 192
)

// drainLogSelector is the project's database container only: the same pods
// the Logs page reads, without the operator's sidecars.
func drainLogSelector(inst *domain.DatabaseInstance) string {
	if inst.DBType == domain.MySQL {
		return mysqlLogSelector(inst.Namespace, inst.ProjectID)
	}
	return fmt.Sprintf("{namespace=%s,cnpg_io_cluster=%s,container=%s}", strconv.Quote(inst.Namespace),
		strconv.Quote(inst.ProjectID+postgresSuffix), strconv.Quote("postgres"))
}

// Pull leases at most one batch for each of up to limit due drains, within
// one answer's byte budget.
func (s *LogDrainService) Pull(ctx context.Context, limit int) ([]logdrain.Batch, error) {
	limit = min(max(limit, 1), logdrain.MaxDrainsPerPull)
	now := s.now()
	due, err := s.store.DueLogDrains(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	// Loki reads share one deadline; store writes keep the caller's context.
	readCtx, cancel := context.WithTimeout(ctx, logPullDeadline)
	defer cancel()
	budget := logdrain.MaxPullBytes
	batches := make([]logdrain.Batch, 0, len(due))
	started := time.Now()
	for i := range due {
		if i > 0 && time.Since(started) >= s.pullTime {
			break
		}
		batch, size, ok := s.batchFor(ctx, readCtx, &due[i], now)
		if !ok {
			continue
		}
		if size > budget {
			// Left due: it leads the next pull.
			break
		}
		leased, err := s.store.LeaseLogDrain(ctx, domain.LogDrainLease{ID: batch.DrainID, From: batch.From, To: batch.To,
			Lines: len(batch.Lines), Until: now.Add(logDrainLease), Now: now})
		if err != nil || !leased {
			continue
		}
		budget -= size
		batches = append(batches, batch)
	}
	return batches, nil
}

func (s *LogDrainService) batchFor(ctx, readCtx context.Context, drain *domain.LogDrain, now time.Time) (logdrain.Batch, int, bool) {
	batch, selector, err := s.prepare(drain)
	if err != nil {
		s.recordFailure(ctx, drain, 0, err.Error(), now)
		return logdrain.Batch{}, 0, false
	}
	end := now.Add(-s.ingestDelay).UnixNano()
	from := drain.CursorNS
	if end <= from {
		s.deferDrain(ctx, drain.ID, time.Unix(0, from).Add(s.ingestDelay+time.Second))
		return logdrain.Batch{}, 0, false
	}
	if from, err = s.skipBacklog(ctx, readCtx, drain, selector, from, end); err != nil {
		log.Printf("log drain %s: skip ahead: %v", drain.ID, err)
		s.deferDrain(ctx, drain.ID, now.Add(logStoreRetry))
		return logdrain.Batch{}, 0, false
	}
	lines, err := s.logs.ReadLines(readCtx, selector, from+1, end, logdrain.MaxBatchLines)
	if err != nil {
		log.Printf("log drain %s: read logs: %v", drain.ID, err)
		s.deferDrain(ctx, drain.ID, now.Add(logStoreRetry))
		return logdrain.Batch{}, 0, false
	}
	lines, size, cut := boundBatch(lines)
	if len(lines) == 0 {
		s.advanceIdle(ctx, drain.ID, from, end, now)
		return logdrain.Batch{}, 0, false
	}
	batch.From, batch.To, batch.Lines = from, end, lines
	if cut || len(lines) == logdrain.MaxBatchLines {
		batch.Lines = endOnWholeTimestamp(lines)
		batch.To = batch.Lines[len(batch.Lines)-1].TS
	}
	return batch, size, true
}

var errLogsNotForwardable = errors.New("the project's logs are not available for forwarding")

// prepare resolves where the drain's batches go and the project's selector.
func (s *LogDrainService) prepare(drain *domain.LogDrain) (logdrain.Batch, string, error) {
	inst, err := s.instances(drain.ProjectID)
	if err != nil {
		return logdrain.Batch{}, "", errLogsNotForwardable
	}
	selector, ok := s.drainSelector(inst)
	if !ok {
		return logdrain.Batch{}, "", errLogsNotForwardable
	}
	entry, err := s.readEntry(drain)
	if err != nil {
		log.Printf("log drain %s: %v", drain.ID, err)
		return logdrain.Batch{}, "", errors.New("the drain's key could not be read")
	}
	target := entry.Endpoint
	if drain.Kind == logdrain.KindDatadog {
		if target, err = logdrain.DatadogIntakeURL(drain.DatadogSite); err != nil {
			return logdrain.Batch{}, "", errors.New("the drain's Datadog site is not supported")
		}
	}
	if target == "" || (entry.Secret == "" && (drain.Kind == logdrain.KindDatadog || drain.HeaderName != "")) {
		return logdrain.Batch{}, "", errors.New("the drain's key or endpoint is missing; save it again")
	}
	return logdrain.Batch{DrainID: drain.ID, ProjectID: drain.ProjectID, Kind: drain.Kind, Target: target,
		HeaderName: drain.HeaderName, Secret: entry.Secret}, selector, nil
}

// skipBacklog moves a drain more than logMaxLag behind to just before end,
// counting the lines it skips as dropped.
func (s *LogDrainService) skipBacklog(ctx, readCtx context.Context, drain *domain.LogDrain, selector string, from, end int64) (int64, error) {
	if time.Duration(end-from) <= logMaxLag {
		return from, nil
	}
	resume := end - int64(logResumeWindow)
	dropped, err := s.logs.CountLines(readCtx, selector, from+1, resume+1)
	if err != nil {
		log.Printf("log drain %s: count skipped lines: %v", drain.ID, err)
		dropped = 0
	}
	moved, err := s.store.AdvanceLogDrain(ctx, domain.LogDrainAdvance{ID: drain.ID, From: from, To: resume, Dropped: dropped, Next: s.now()})
	if err != nil {
		return 0, err
	}
	if !moved {
		return 0, errors.New("the drain moved meanwhile")
	}
	return resume, nil
}

// boundBatch keeps lines while their encoded size stays under MaxBatchBytes,
// and reports that size and whether it cut any.
func boundBatch(lines []logdrain.Line) ([]logdrain.Line, int, bool) {
	size := 0
	for i, line := range lines {
		next := logdrain.EncodedSize(line.Line) + lineEntryOverhead
		if size+next > logdrain.MaxBatchBytes && i > 0 {
			return lines[:i], size, true
		}
		size += next
	}
	return lines, size, false
}

// endOnWholeTimestamp drops the last timestamp's lines from a cut batch, so
// lines sharing it beyond the cut are read with them next time. A batch of
// one timestamp is kept whole.
func endOnWholeTimestamp(lines []logdrain.Line) []logdrain.Line {
	last := lines[len(lines)-1].TS
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].TS != last {
			return lines[:i+1]
		}
	}
	return lines
}

func (s *LogDrainService) advanceIdle(ctx context.Context, id string, from, end int64, now time.Time) {
	if _, err := s.store.AdvanceLogDrain(ctx, domain.LogDrainAdvance{ID: id, From: from, To: end, Next: now.Add(logIdlePoll)}); err != nil {
		log.Printf("log drain %s: advance: %v", id, err)
	}
}

func (s *LogDrainService) deferDrain(ctx context.Context, id string, next time.Time) {
	if err := s.store.DeferLogDrain(ctx, id, next); err != nil {
		log.Printf("log drain %s: defer: %v", id, err)
	}
}

// Ack settles the batch the drain leased. An ack for another batch, a drain
// that moved on or one that was deleted is ignored.
func (s *LogDrainService) Ack(ctx context.Context, ack logdrain.Ack) error {
	now := s.now()
	if ack.To <= ack.From || ack.To > now.UnixNano() || ack.Lines < 0 || ack.Lines > logdrain.MaxBatchLines {
		return ErrInvalidLogDrainAck
	}
	drain, err := s.store.FindLogDrain(ctx, ack.DrainID)
	if errors.Is(err, domain.ErrLogDrainNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !drain.Enabled || drain.LeaseTo == 0 || drain.LeaseTo != ack.To || drain.CursorNS != ack.From || ack.Lines > drain.LeaseLines {
		return nil
	}
	if ack.Error != "" {
		entry, _ := s.readEntry(drain)
		s.recordFailure(ctx, drain, drain.LeaseTo, logdrain.Redact(ack.Error, entry.Secret), now)
		return nil
	}
	next := now.Add(logIdlePoll)
	if ack.Lines == logdrain.MaxBatchLines {
		next = now
	}
	_, err = s.store.AdvanceLogDrain(ctx, domain.LogDrainAdvance{ID: drain.ID, From: ack.From, To: ack.To,
		Sent: int64(ack.Lines), Delivered: true, Next: next})
	return err
}

func (s *LogDrainService) recordFailure(ctx context.Context, drain *domain.LogDrain, leaseTo int64, message string, now time.Time) {
	failures := drain.ConsecutiveFailures + 1
	failure := domain.LogDrainFailure{ID: drain.ID, From: drain.CursorNS, LeaseTo: leaseTo, Error: logdrain.Redact(message, ""),
		Next: now.Add(logDrainBackoff(failures)), Disable: failures >= MaxLogDrainFailures}
	if _, err := s.store.FailLogDrain(ctx, failure); err != nil {
		log.Printf("log drain %s: record failure: %v", drain.ID, err)
	}
}

// logDrainBackoff doubles from 30 seconds per failure in a row, up to 30 minutes.
func logDrainBackoff(failures int) time.Duration {
	wait := minLogDrainWait
	for i := 1; i < failures && wait < maxLogDrainBackoff; i++ {
		wait *= 2
	}
	return min(wait, maxLogDrainBackoff)
}
