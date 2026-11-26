package credits

import (
	"math/rand"
	"testing"
	"time"
)

var drawNow = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

func lotExpiring(id int64, remaining Micros, in time.Duration) Lot {
	at := drawNow.Add(in)
	return Lot{ID: id, Remaining: remaining, ExpiresAt: &at}
}

func TestPlanDrawTakesTheSoonestExpiringLotFirst(t *testing.T) {
	lots := []Lot{
		{ID: 1, Remaining: 50},
		lotExpiring(2, 50, 48*time.Hour),
		lotExpiring(3, 50, 24*time.Hour),
	}
	draws, taken := PlanDraw(lots, 70, drawNow.Add(time.Hour))
	if taken != 70 {
		t.Fatalf("taken %d, want 70", taken)
	}
	want := []Draw{{LotID: 3, Amount: 50}, {LotID: 2, Amount: 20}}
	if len(draws) != len(want) || draws[0] != want[0] || draws[1] != want[1] {
		t.Fatalf("draws %+v, want %+v", draws, want)
	}
}

func TestPlanDrawKeepsLotsThatExpireBeforeThePeriodEnds(t *testing.T) {
	lots := []Lot{lotExpiring(1, 100, time.Hour), lotExpiring(2, 100, 10*time.Hour)}
	draws, taken := PlanDraw(lots, 150, drawNow.Add(5*time.Hour))
	if taken != 100 || len(draws) != 1 || draws[0].LotID != 2 {
		t.Fatalf("a lot expiring before the period end must not pay for it: draws %+v taken %d", draws, taken)
	}
}

func TestPlanDrawAcceptsALotExpiringExactlyAtThePeriodEnd(t *testing.T) {
	lots := []Lot{lotExpiring(1, 40, 5*time.Hour)}
	_, taken := PlanDraw(lots, 40, drawNow.Add(5*time.Hour))
	if taken != 40 {
		t.Fatalf("a lot expiring at the period end covers it: taken %d", taken)
	}
}

func TestPlanDrawNeverTakesMoreThanTheLotsHold(t *testing.T) {
	lots := []Lot{{ID: 1, Remaining: 30}, {ID: 2, Remaining: 20}}
	draws, taken := PlanDraw(lots, 1000, drawNow)
	if taken != 50 {
		t.Fatalf("taken %d, want 50", taken)
	}
	var sum Micros
	for _, draw := range draws {
		sum += draw.Amount
	}
	if sum != taken {
		t.Fatalf("draws add to %d, taken %d", sum, taken)
	}
}

func TestPlanDrawSkipsEmptyLots(t *testing.T) {
	draws, taken := PlanDraw([]Lot{{ID: 1, Remaining: 0}}, 10, drawNow)
	if taken != 0 || len(draws) != 0 {
		t.Fatalf("an empty lot gave %+v %d", draws, taken)
	}
}

func TestPlanDrawProperty(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	for round := 0; round < 500; round++ {
		lots := make([]Lot, random.Intn(6))
		var usable Micros
		periodEnd := drawNow.Add(time.Duration(random.Intn(100)) * time.Hour)
		for i := range lots {
			lots[i] = Lot{ID: int64(i + 1), Remaining: Micros(random.Intn(1000))}
			if random.Intn(3) > 0 {
				at := drawNow.Add(time.Duration(random.Intn(200)) * time.Hour)
				lots[i].ExpiresAt = &at
			}
			if lots[i].ExpiresAt == nil || !lots[i].ExpiresAt.Before(periodEnd) {
				usable += lots[i].Remaining
			}
		}
		want := Micros(random.Intn(3000))
		draws, taken := PlanDraw(lots, want, periodEnd)
		if taken != min(want, usable) {
			t.Fatalf("round %d: taken %d, want min(%d, %d)", round, taken, want, usable)
		}
		var sum Micros
		for _, draw := range draws {
			sum += draw.Amount
			if draw.Amount <= 0 {
				t.Fatalf("round %d: empty draw %+v", round, draw)
			}
		}
		if sum != taken {
			t.Fatalf("round %d: draws add to %d, taken %d", round, sum, taken)
		}
	}
}
