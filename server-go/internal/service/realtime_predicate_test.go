package service

import (
	"strings"
	"testing"
)

func TestTheListingUsesTheSamePredicateAsTheFunction(t *testing.T) {
	if !strings.Contains(listRealtimeTablesSQL, realtimeUserSchemaPredicate) {
		t.Fatal("the realtime listing and set_realtime_table must exclude the same schemas")
	}
}
