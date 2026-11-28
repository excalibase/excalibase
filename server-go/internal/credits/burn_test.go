package credits

import (
	"testing"
	"time"
)

var burnNow = time.Date(2026, 12, 8, 15, 30, 0, 0, time.UTC)

func day(offset int) time.Time {
	return time.Date(2026, 12, 8, 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset)
}

func TestTheBurnListsEverySevenDayEvenWhenAnEmptyOneHadNoUsage(t *testing.T) {
	burn := ComputeBurn(UsageBurn{Days: []DayBurn{{Day: day(-2), Micros: 5_000_000}}, Since: ptr(day(-2))}, 20_000_000, burnNow)
	if len(burn.Days) != 7 || burn.Days[0].Day != "2026-12-02" || burn.Days[6].Day != "2026-12-08" {
		t.Fatalf("seven UTC days ending today, got %+v", burn.Days)
	}
	if burn.Days[4].Micros != 5_000_000 || burn.Days[3].Micros != 0 {
		t.Fatalf("the day with usage and the days without, got %+v", burn.Days)
	}
}

func TestTheDailyRateIsTheUsageOverTheTimeObserved(t *testing.T) {
	// 6 dollars over the 36 hours since the first usage = 4 dollars a day.
	since := burnNow.Add(-36 * time.Hour)
	burn := ComputeBurn(UsageBurn{Days: []DayBurn{{Day: day(-1), Micros: 2_000_000}, {Day: day(0), Micros: 4_000_000}}, Since: &since}, 40_000_000, burnNow)
	if burn.PerDay != 4_000_000 {
		t.Fatalf("6,000,000 over 36 hours is 4,000,000 a day, got %d", burn.PerDay)
	}
}

func TestTheRateNeverUsesMoreThanTheSevenDayWindow(t *testing.T) {
	since := burnNow.Add(-60 * 24 * time.Hour)
	week := UsageBurn{Days: []DayBurn{{Day: day(-3), Micros: 7_000_000}}, Since: &since}
	got := ComputeBurn(week, 100_000_000, burnNow).PerDay
	windowStart := day(-6)
	want := Micros(7_000_000 * 86_400 / int64(burnNow.Sub(windowStart)/time.Second))
	if got != want {
		t.Fatalf("the window starts at %v: want %d, got %d", windowStart, want, got)
	}
}

func TestAnHourOfUsageIsNotScaledByLessThanAnHour(t *testing.T) {
	since := burnNow.Add(-5 * time.Minute)
	burn := ComputeBurn(UsageBurn{Days: []DayBurn{{Day: day(0), Micros: 1000}}, Since: &since}, 1_000_000, burnNow)
	if burn.PerDay != 24_000 {
		t.Fatalf("observed time is at least one hour: 1000 an hour is 24000 a day, got %d", burn.PerDay)
	}
}

func TestTheRunOutIsTheBalanceOverTheDailyRate(t *testing.T) {
	since := burnNow.Add(-24 * time.Hour)
	burn := ComputeBurn(UsageBurn{Days: []DayBurn{{Day: day(0), Micros: 2_000_000}}, Since: &since}, 10_000_000, burnNow)
	if burn.RunOutAt == nil || !burn.RunOutAt.Equal(burnNow.Add(5*24*time.Hour)) {
		t.Fatalf("2 dollars a day against 10 dollars is five days away, got %v", burn.RunOutAt)
	}
}

func TestNothingBurnedOrNothingLeftHasNoRunOutDate(t *testing.T) {
	since := burnNow.Add(-24 * time.Hour)
	used := UsageBurn{Days: []DayBurn{{Day: day(0), Micros: 2_000_000}}, Since: &since}
	if got := ComputeBurn(UsageBurn{}, 10_000_000, burnNow); got.PerDay != 0 || got.RunOutAt != nil {
		t.Fatalf("no usage: %+v", got)
	}
	if got := ComputeBurn(used, 0, burnNow); got.RunOutAt != nil {
		t.Fatalf("a zero balance has already run out: %+v", got)
	}
}

func TestAnAbsurdRunOutIsLeftOut(t *testing.T) {
	since := burnNow.Add(-24 * time.Hour)
	burn := ComputeBurn(UsageBurn{Days: []DayBurn{{Day: day(0), Micros: 1}}, Since: &since}, 9_000_000_000_000, burnNow)
	if burn.RunOutAt != nil {
		t.Fatalf("more than ten years away is not a date worth showing: %v", burn.RunOutAt)
	}
}

func ptr(at time.Time) *time.Time { return &at }
