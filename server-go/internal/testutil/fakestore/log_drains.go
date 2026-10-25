package fakestore

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// LogDrains is an in-memory log drain store with the Postgres store's
// compare-and-set semantics.
type LogDrains struct {
	mu    sync.Mutex
	Items map[string]domain.LogDrain
	Err   error
}

func NewLogDrains() *LogDrains { return &LogDrains{Items: map[string]domain.LogDrain{}} }

func (s *LogDrains) CreateLogDrain(_ context.Context, drain *domain.LogDrain, limit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	count := 0
	for _, existing := range s.Items {
		if existing.ProjectID == drain.ProjectID {
			count++
		}
	}
	if count >= limit {
		return domain.ErrLogDrainLimit
	}
	s.Items[drain.ID] = *drain
	return nil
}

func (s *LogDrains) ListLogDrains(_ context.Context, projectID string) ([]domain.LogDrain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	out := []domain.LogDrain{}
	for _, drain := range s.Items {
		if drain.ProjectID == projectID {
			out = append(out, drain)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *LogDrains) GetLogDrain(_ context.Context, projectID, id string) (*domain.LogDrain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	drain, ok := s.Items[id]
	if !ok || drain.ProjectID != projectID {
		return nil, domain.ErrLogDrainNotFound
	}
	return &drain, nil
}

func (s *LogDrains) FindLogDrain(_ context.Context, id string) (*domain.LogDrain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	drain, ok := s.Items[id]
	if !ok {
		return nil, domain.ErrLogDrainNotFound
	}
	return &drain, nil
}

func (s *LogDrains) UpdateLogDrain(_ context.Context, drain *domain.LogDrain) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.Items[drain.ID]
	if !ok || existing.ProjectID != drain.ProjectID {
		return domain.ErrLogDrainNotFound
	}
	existing.Name, existing.DatadogSite, existing.URL, existing.HeaderName, existing.UpdatedAt =
		drain.Name, drain.DatadogSite, drain.URL, drain.HeaderName, drain.UpdatedAt
	s.Items[drain.ID] = existing
	return nil
}

func (s *LogDrains) SetLogDrainEnabled(_ context.Context, projectID, id string, enabled bool, cursor int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	drain, ok := s.Items[id]
	if !ok || drain.ProjectID != projectID {
		return domain.ErrLogDrainNotFound
	}
	drain.Enabled, drain.LastError, drain.ConsecutiveFailures, drain.LeaseTo, drain.LeaseLines = enabled, "", 0, 0, 0
	drain.NextAttemptAt, drain.UpdatedAt = at, at
	drain.Status = domain.LogDrainDisabled
	if enabled {
		drain.Status, drain.CursorNS = domain.LogDrainActive, cursor
	}
	s.Items[id] = drain
	return nil
}

func (s *LogDrains) LeaseLogDrain(_ context.Context, lease domain.LogDrainLease) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	drain, ok := s.Items[lease.ID]
	if !ok || !drain.Enabled || drain.CursorNS != lease.From || drain.NextAttemptAt.After(lease.Now) {
		return false, nil
	}
	drain.LeaseTo, drain.LeaseLines, drain.NextAttemptAt = lease.To, lease.Lines, lease.Until
	s.Items[lease.ID] = drain
	return true, nil
}

func (s *LogDrains) DeleteLogDrain(_ context.Context, projectID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	drain, ok := s.Items[id]
	if !ok || drain.ProjectID != projectID {
		return domain.ErrLogDrainNotFound
	}
	delete(s.Items, id)
	return nil
}

func (s *LogDrains) DueLogDrains(_ context.Context, now time.Time, limit int) ([]domain.LogDrain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	out := []domain.LogDrain{}
	for _, drain := range s.Items {
		if drain.Enabled && !drain.NextAttemptAt.After(now) {
			out = append(out, drain)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextAttemptAt.Before(out[j].NextAttemptAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *LogDrains) AdvanceLogDrain(_ context.Context, adv domain.LogDrainAdvance) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	drain, ok := s.Items[adv.ID]
	if !ok || !drain.Enabled || drain.CursorNS != adv.From || (adv.Delivered && drain.LeaseTo != adv.To) {
		return false, nil
	}
	drain.LeaseTo, drain.LeaseLines = 0, 0
	drain.CursorNS = adv.To
	drain.SentLines += adv.Sent
	drain.DroppedLines += adv.Dropped
	drain.NextAttemptAt = adv.Next
	if adv.Delivered {
		now := time.Now()
		drain.ConsecutiveFailures, drain.LastError, drain.Status, drain.LastSuccessAt = 0, "", domain.LogDrainActive, &now
	}
	s.Items[adv.ID] = drain
	return true, nil
}

func (s *LogDrains) FailLogDrain(_ context.Context, failure domain.LogDrainFailure) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	drain, ok := s.Items[failure.ID]
	if !ok || !drain.Enabled || drain.CursorNS != failure.From || (failure.LeaseTo != 0 && drain.LeaseTo != failure.LeaseTo) {
		return false, nil
	}
	drain.LeaseTo, drain.LeaseLines = 0, 0
	drain.ConsecutiveFailures++
	drain.LastError = failure.Error
	drain.NextAttemptAt = failure.Next
	drain.Status = domain.LogDrainFailing
	if failure.Disable {
		drain.Status, drain.Enabled = domain.LogDrainDisabled, false
	}
	s.Items[failure.ID] = drain
	return true, nil
}

func (s *LogDrains) DeferLogDrain(_ context.Context, id string, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if drain, ok := s.Items[id]; ok {
		drain.NextAttemptAt = next
		s.Items[id] = drain
	}
	return nil
}
