package credits

import (
	"context"
	"time"
)

// lowCreditPercent is the share of the sign-up credit under which Studio warns that credit is running low.
const lowCreditPercent = 20

// PausedService is a service paused because the credit ran out.
type PausedService struct {
	Kind      string    `json:"kind"`
	ProjectID string    `json:"projectId"`
	ServiceID string    `json:"serviceId"`
	Name      string    `json:"name"`
	PausedAt  time.Time `json:"pausedAt"`
}

// ZeroInfo is what the org's credit state is, as the store keeps it.
type ZeroInfo struct {
	AtZero bool
	// Since is when the credit ran out; nil until the first pass saw it.
	Since  *time.Time
	Paused []PausedService
}

// ZeroSource reads where an org stands when its credit runs out.
type ZeroSource interface {
	OrgZero(ctx context.Context, orgID string) (ZeroInfo, error)
}

// ZeroView is what Studio shows of credit running low, used up, and what that paused.
type ZeroView struct {
	AtZero    bool `json:"atZero"`
	LowCredit bool `json:"lowCredit"`
	// GraceEndsAt is the day compute above the free allowance pauses; nil while the org has credit.
	GraceEndsAt *time.Time      `json:"graceEndsAt"`
	GraceDays   int             `json:"graceDays"`
	Paused      []PausedService `json:"paused"`
}

// SetZero turns on the $0 section of the overview: with it, the overview says when
// compute will pause and which services already did. Set once at boot.
func (s *Service) SetZero(source ZeroSource, grace time.Duration) {
	s.zero, s.grace = source, grace
}

func (s *Service) zeroView(ctx context.Context, orgID string, balance Micros, now time.Time) (*ZeroView, error) {
	if s.zero == nil {
		return nil, nil
	}
	info, err := s.zero.OrgZero(ctx, orgID)
	if err != nil {
		return nil, err
	}
	view := &ZeroView{AtZero: info.AtZero, GraceDays: int(s.grace / (24 * time.Hour)), Paused: info.Paused}
	if view.Paused == nil {
		view.Paused = []PausedService{}
	}
	view.LowCredit = balance > 0 && balance*100 <= s.rules.TrialAmount*lowCreditPercent
	if info.AtZero {
		start := now
		if info.Since != nil {
			start = *info.Since
		}
		ends := start.Add(s.grace)
		view.GraceEndsAt = &ends
	}
	return view, nil
}
