package creditmeter

import (
	"context"
	"errors"
	"testing"
)

type fakeLeader struct {
	leading bool
	err     error
}

func (f fakeLeader) IsLeader(context.Context) (bool, error) { return f.leading, f.err }

type counter struct{ samples, meters int }

func (c *counter) sample(context.Context) error { c.samples++; return nil }
func (c *counter) meter(context.Context) error  { c.meters++; return nil }

func TestOnlyTheLeadingReplicaSamplesAndMeters(t *testing.T) {
	var ran counter
	NewRunner(fakeLeader{leading: false}, ran.sample, ran.meter).Tick(context.Background())
	if ran.samples+ran.meters != 0 {
		t.Fatalf("a follower must do nothing, got %+v", ran)
	}
	NewRunner(fakeLeader{leading: true}, ran.sample, ran.meter).Tick(context.Background())
	if ran.samples != 1 || ran.meters != 1 {
		t.Fatalf("the leader samples then meters once, got %+v", ran)
	}
}

func TestAFailedLeaderCheckSkipsTheTick(t *testing.T) {
	var ran counter
	NewRunner(fakeLeader{leading: true, err: errors.New("lock lost")}, ran.sample, ran.meter).Tick(context.Background())
	if ran.samples+ran.meters != 0 {
		t.Fatalf("an unknown leadership must not run, got %+v", ran)
	}
}

func TestAFailedSampleDoesNotStopTheMeter(t *testing.T) {
	var ran counter
	failing := func(context.Context) error { ran.samples++; return errors.New("boom") }
	NewRunner(fakeLeader{leading: true}, failing, ran.meter).Tick(context.Background())
	if ran.meters != 1 {
		t.Fatal("billing settled hours must not wait on a sample that failed")
	}
}
