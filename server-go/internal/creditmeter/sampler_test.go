package creditmeter

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSampleStore struct {
	orgs    []string
	sampled []string
	failFor string
}

func (f *fakeSampleStore) SampledOrgs(context.Context) ([]string, error) { return f.orgs, nil }
func (f *fakeSampleStore) SampleOrg(_ context.Context, orgID string) error {
	if orgID == f.failFor {
		return errors.New("size unreadable")
	}
	f.sampled = append(f.sampled, orgID)
	return nil
}

func TestAPassSamplesEveryOrgAndReportsTheOnesThatFailed(t *testing.T) {
	store := &fakeSampleStore{orgs: []string{"a", "b", "c"}, failFor: "b"}
	err := NewSampler(store).Pass(context.Background())
	if err == nil {
		t.Fatal("the org that failed must be reported")
	}
	if len(store.sampled) != 2 || store.sampled[0] != "a" || store.sampled[1] != "c" {
		t.Fatalf("a and c are still sampled, got %v", store.sampled)
	}
}

func TestTheNextSampleLandsInTheNextSlotAtARandomPoint(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 7, 30, 0, time.UTC) // 2m30s before the 12:10 slot
	for _, offset := range []int64{0, 1, int64(SlotLength) - 1} {
		delay := nextDelay(now, func(int64) int64 { return offset })
		landing := now.Add(delay)
		slotStart := time.Date(2026, 12, 1, 12, 10, 0, 0, time.UTC)
		if landing.Before(slotStart) || !landing.Before(slotStart.Add(SlotLength)) {
			t.Fatalf("offset %d lands at %v, outside the 12:10 slot", offset, landing)
		}
		if got := landing.Sub(slotStart); got != time.Duration(offset) {
			t.Fatalf("offset %d: landed %v into the slot", offset, got)
		}
	}
}

func TestTheOffsetIsDrawnFromTheWholeSlot(t *testing.T) {
	var asked int64
	nextDelay(day, func(n int64) int64 { asked = n; return 0 })
	if asked != int64(SlotLength) {
		t.Fatalf("the offset must be drawn below one slot (%d), asked for %d", int64(SlotLength), asked)
	}
}

func TestOnASlotBoundaryTheNextSampleIsInTheFollowingSlot(t *testing.T) {
	delay := nextDelay(day.Add(12*time.Hour+10*time.Minute), func(int64) int64 { return 0 })
	if delay != SlotLength {
		t.Fatalf("from exactly 12:10 the next slot starts in %v, got %v", SlotLength, delay)
	}
}
