package auth

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/testutil"
)

func TestGateSlotsAreAShareOfMemoryAndCPU(t *testing.T) {
	const gib = 1 << 30
	cases := []struct {
		name string
		mem  uint64
		cpus int
		want int
	}{
		{"1 GiB pod, 2 cpus: memory share allows 4, cpu share 1", 1 * gib, 2, 1},
		{"1 GiB pod, 8 cpus: memory is the cap", 1 * gib, 8, 4},
		{"4 GiB pod, 8 cpus: cpu share is the cap", 4 * gib, 8, 4},
		{"4 GiB pod, 32 cpus: memory share is the cap", 4 * gib, 32, 16},
		{"tiny pod still gets one slot", 64 << 20, 1, 1},
		{"unknown memory falls back to cpu share", 0, 4, 2},
	}
	for _, tc := range cases {
		if got := gateSlots(tc.mem, tc.cpus); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestGateRefusesWhenEverySlotStaysTaken(t *testing.T) {
	g := newHashGate(1, 20*time.Millisecond)
	release := make(chan struct{})
	started := make(chan struct{})
	go func() { _ = g.run(func() { close(started); <-release }) }()
	<-started

	begin := time.Now()
	err := g.run(func() { t.Error("ran past a full gate") })
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
	if time.Since(begin) > time.Second {
		t.Errorf("refusal took %v, must be fast", time.Since(begin))
	}
	close(release)
}

func TestGateNeverRunsMoreThanItsSlots(t *testing.T) {
	const slots = 3
	g := newHashGate(slots, time.Second)
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = g.run(func() {
				now := running.Add(1)
				for {
					old := peak.Load()
					if now <= old || peak.CompareAndSwap(old, now) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				running.Add(-1)
			})
		}()
	}
	wg.Wait()
	if peak.Load() > slots {
		t.Fatalf("peak concurrency %d exceeds %d slots", peak.Load(), slots)
	}
}

// The same bound covers hashing and verifying: a saturated gate refuses both.
func TestPasswordFunctionsRefuseWhenTheGateIsFull(t *testing.T) {
	hash, err := HashPassword(testutil.FixturePassword("gate-seed"))
	if err != nil {
		t.Fatalf("seed hash: %v", err)
	}
	restore := ConfigurePasswordGate(0, time.Millisecond)
	defer restore()

	if _, err := HashPassword(testutil.FixturePassword("gate-hash")); !errors.Is(err, ErrBusy) {
		t.Errorf("HashPassword: got %v, want ErrBusy", err)
	}
	if _, err := VerifyPassword(testutil.FixturePassword("gate-seed"), hash, true); !errors.Is(err, ErrBusy) {
		t.Errorf("VerifyPassword: got %v, want ErrBusy", err)
	}
}

// Unknown usernames can fill their share of the gate but never the slots kept
// for accounts that exist, so a flood cannot lock a real admin out (EXC-749).
func TestGateKeepsSlotsForKnownAccountsWhenUnknownNamesFloodIt(t *testing.T) {
	g := newHashGate(4, 20*time.Millisecond)
	release := make(chan struct{})
	var started sync.WaitGroup
	for i := 0; i < 3; i++ {
		started.Add(1)
		go func() { _ = g.runFor(false, func() { started.Done(); <-release }) }()
	}
	started.Wait()

	if err := g.runFor(false, func() { t.Error("an unknown name took a reserved slot") }); !errors.Is(err, ErrBusy) {
		t.Fatalf("fourth unknown: got %v, want ErrBusy", err)
	}
	ran := false
	if err := g.runFor(true, func() { ran = true }); err != nil || !ran {
		t.Fatalf("known account refused behind a flood: err=%v ran=%v", err, ran)
	}
	close(release)
}

func TestGateReservesAQuarterOfTheSlotsAndAtLeastOne(t *testing.T) {
	cases := map[int]int{2: 1, 3: 1, 4: 1, 8: 2, 16: 4}
	for slots, reserved := range cases {
		if got := reservedSlots(slots); got != reserved {
			t.Errorf("%d slots: reserved %d, want %d", slots, got, reserved)
		}
	}
}

// One slot cannot be split: unknown names must still be able to sign in
// (and be refused) there, or the gate would answer them differently.
func TestGateWithOneSlotServesUnknownNamesToo(t *testing.T) {
	g := newHashGate(1, 20*time.Millisecond)
	ran := false
	if err := g.runFor(false, func() { ran = true }); err != nil || !ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
}
