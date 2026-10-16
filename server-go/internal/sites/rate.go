package sites

import (
	"context"
	"errors"
	"time"
)

// deployRateWindow is the span the plan's deploys-per-hour limit counts over.
const deployRateWindow = time.Hour

// ErrDeployRateLimited is a project past its plan's deploys per hour.
var ErrDeployRateLimited = errors.New("this project has started as many deploys this hour as its plan allows")

// RateLimitError says when the project may deploy again.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return ErrDeployRateLimited.Error() + "; try again in " + e.RetryAfter.Round(time.Second).String()
}

func (e *RateLimitError) Is(target error) bool { return target == ErrDeployRateLimited }

// takeDeploySlot counts this upload against the project's deploys per hour
// before its body is read, so a refused archive counts as much as a good one.
func (s *Service) takeDeploySlot(ctx context.Context, projectID string, perHour int) error {
	if perHour <= 0 {
		return &RateLimitError{RetryAfter: deployRateWindow}
	}
	wait, err := s.store.TakeDeploySlot(ctx, projectID, s.now().UTC(), deployRateWindow, perHour)
	if err != nil {
		return err
	}
	if wait > 0 {
		return &RateLimitError{RetryAfter: max(wait, time.Second)}
	}
	return nil
}
