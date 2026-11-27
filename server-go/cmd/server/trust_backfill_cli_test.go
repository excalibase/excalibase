package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/trust"
)

type countingGrandfather struct{ granted int }

func (c countingGrandfather) GrandfatherUsersBefore(context.Context, time.Time) (int, error) {
	return c.granted, nil
}

func (countingGrandfather) RecordBackfill(context.Context, trust.BackfillRun) error { return nil }

func TestRunTrustBackfillSaysWhoWasGrandfatheredAndWhoWasNot(t *testing.T) {
	var out bytes.Buffer
	cutoff := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := runTrustBackfill(context.Background(), countingGrandfather{granted: 278}, cutoff, time.Now(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"278", "2026-11-01", "must verify"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report does not say %q: %s", want, out.String())
		}
	}
}

func TestTrustBackfillIsACommand(t *testing.T) {
	if !strings.Contains(helpText(), "trust-backfill") {
		t.Error("the help does not list trust-backfill")
	}
}
