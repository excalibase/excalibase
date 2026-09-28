package storagebudget

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const gi = int64(1) << 30

type fakeSource struct {
	mu        sync.Mutex
	capacity  int64
	capErr    error
	allocated k8s.StorageAllocation
	allocErr  error
}

func (f *fakeSource) Capacity(context.Context) (int64, error) { return f.capacity, f.capErr }
func (f *fakeSource) StorageAllocated(context.Context) (k8s.StorageAllocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allocated, f.allocErr
}
func (f *fakeSource) add(bytes int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allocated.TenantBytes += bytes
}

func TestReport(t *testing.T) {
	source := &fakeSource{capacity: 100 * gi, allocated: k8s.StorageAllocation{TenantBytes: 50 * gi, PlatformBytes: 6 * gi}}
	report, err := New(source, 80).Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.CapacityBytes != 100*gi || report.BudgetBytes != 80*gi || report.AllocatedBytes != 56*gi || report.FreeBytes != 24*gi || report.Percent != 80 {
		t.Fatalf("report = %+v", report)
	}
	if report.UsedPercent != 70 {
		t.Fatalf("used = %v%% of the budget, want 70", report.UsedPercent)
	}
}

func TestCheckRefusesWhatWouldPassTheBudget(t *testing.T) {
	budget := New(&fakeSource{capacity: 100 * gi, allocated: k8s.StorageAllocation{TenantBytes: 76 * gi}}, 80)
	if err := budget.Check(context.Background(), 4*gi, "a 4Gi disk"); err != nil {
		t.Fatalf("exactly at the budget: %v", err)
	}
	err := budget.Check(context.Background(), 5*gi, "a project's database (1 x 5Gi)")
	var exceeded *ExceededError
	if !errors.Is(err, ErrExceeded) || !errors.As(err, &exceeded) {
		t.Fatalf("err = %v, want ErrExceeded", err)
	}
	for _, part := range []string{"a project's database (1 x 5Gi)", "needs 5Gi", "4Gi is left"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("%q does not name %q", err.Error(), part)
		}
	}
}

// An unknown capacity or allocation refuses: unanswered is never free space.
func TestCheckFailsClosed(t *testing.T) {
	for name, source := range map[string]*fakeSource{
		"capacity":   {capErr: errors.New("no LVM nodes")},
		"allocation": {capacity: 100 * gi, allocErr: errors.New("api down")},
	} {
		if err := New(source, 80).Check(context.Background(), gi, "x"); err == nil || errors.Is(err, ErrExceeded) {
			t.Errorf("%s: err = %v, want a failure that is not a budget refusal", name, err)
		}
	}
}

// Two allocations checked together cannot both take the last room: the check
// and the allocation happen under one lock, and the allocation is seen at once.
func TestReserveSerialisesAllocations(t *testing.T) {
	source := &fakeSource{capacity: 100 * gi, allocated: k8s.StorageAllocation{TenantBytes: 70 * gi}}
	budget := New(source, 80)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- budget.Reserve(context.Background(), 6*gi, "a disk", func() error { source.add(6 * gi); return nil })
		}()
	}
	wg.Wait()
	close(results)
	var granted, refused int
	for err := range results {
		switch {
		case err == nil:
			granted++
		case errors.Is(err, ErrExceeded):
			refused++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if granted != 1 || refused != 1 {
		t.Fatalf("granted %d, refused %d; want one of each", granted, refused)
	}
}

func TestReserveReturnsTheAllocationsError(t *testing.T) {
	budget := New(&fakeSource{capacity: 100 * gi}, 80)
	boom := errors.New("apply failed")
	if err := budget.Reserve(context.Background(), gi, "x", func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// A nil budget is the unmetered install: nothing is refused, allocations run.
func TestNilBudgetAllowsEverything(t *testing.T) {
	var budget *Budget
	ran := false
	if err := budget.Reserve(context.Background(), 1<<50, "x", func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("err %v ran %v", err, ran)
	}
	if err := budget.Check(context.Background(), 1<<50, "x"); err != nil {
		t.Fatal(err)
	}
	if report, err := budget.Report(context.Background()); err != nil || report.Enabled {
		t.Fatalf("report %+v %v", report, err)
	}
}
