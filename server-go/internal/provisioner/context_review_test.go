package provisioner

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRollback_APanicInACleanupIsThatCleanupsFailure(t *testing.T) {
	pc := NewProvisionContext(nil, nil)
	olderRan := false
	pc.RegisterCleanup("older", func(context.Context) error { olderRan = true; return nil })
	pc.RegisterCleanup("panics", func(context.Context) error { panic("internal detail") })

	results := pc.Rollback(context.Background())

	if !olderRan || len(results) != 2 || results[0].OK || !results[1].OK {
		t.Fatalf("results %+v, older ran=%v", results, olderRan)
	}
	if strings.Contains(results[0].Error, "internal detail") {
		t.Errorf("the result exposes the panic: %q", results[0].Error)
	}
}

func TestRollback_ALastCleanupRunsAfterTheOthersOnlyIfTheyAllWorked(t *testing.T) {
	var order []string
	note := func(name string, err error) CleanupFunc {
		return func(context.Context) error { order = append(order, name); return err }
	}
	pc := NewProvisionContext(nil, nil)
	pc.RegisterCleanup("first", note("first", nil))
	pc.RegisterLastCleanup("record", note("record", nil))
	pc.RegisterCleanup("second", note("second", nil))

	pc.Rollback(context.Background())
	if strings.Join(order, ",") != "second,first,record" {
		t.Fatalf("order %v", order)
	}

	order = nil
	failing := NewProvisionContext(nil, nil)
	failing.RegisterCleanup("first", note("first", errors.New("blip")))
	failing.RegisterLastCleanup("record", note("record", nil))
	results := failing.Rollback(context.Background())
	if strings.Join(order, ",") != "first" {
		t.Fatalf("ran %v", order)
	}
	if len(results) != 2 || results[1].Name != "record" || results[1].OK {
		t.Fatalf("results %+v: the skipped one must say so", results)
	}
}
