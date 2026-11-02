package config

import (
	"reflect"
	"testing"
)

func TestPostgresUpgradeTargetsAreNewerPublishedMajors(t *testing.T) {
	cases := map[string][]string{
		"14": {"16", "17"},
		"15": {"16", "17"},
		"16": {"17"},
		"17": {},
		"":   {},
		"13": {},
		"x":  {},
	}
	for current, want := range cases {
		if got := PostgresUpgradeTargets(current); !reflect.DeepEqual(got, want) {
			t.Errorf("PostgresUpgradeTargets(%q) = %v, want %v", current, got, want)
		}
	}
}

func TestPostgresUpgradeTargetsLeaveOutAnUnpublishedMajor(t *testing.T) {
	previous := postgresCatalog
	defer func() { postgresCatalog = previous }()
	unpublished := previous
	unpublished.Majors = nil
	for _, entry := range previous.Majors {
		if entry.Major == "16" {
			entry.Image = ""
		}
		unpublished.Majors = append(unpublished.Majors, entry)
	}
	postgresCatalog = unpublished
	if got := PostgresUpgradeTargets("14"); !reflect.DeepEqual(got, []string{"17"}) {
		t.Fatalf("PostgresUpgradeTargets(14) = %v, want [17]", got)
	}
}

func TestIsPostgresUpgradeTarget(t *testing.T) {
	if !IsPostgresUpgradeTarget("16", "17") {
		t.Error("17 should be a target from 16")
	}
	for _, target := range []string{"16", "15", "18", ""} {
		if IsPostgresUpgradeTarget("16", target) {
			t.Errorf("%q must not be a target from 16", target)
		}
	}
}

// CloudNativePG 1.30 refuses a major upgrade to anything below 16
// (cluster_webhook.go validateImageChange); a single host offers the same set.
func TestPostgresUpgradeTargetsStartAtSixteen(t *testing.T) {
	for _, target := range PostgresUpgradeTargets("14") {
		if target == "15" {
			t.Fatal("15 is offered as a target; the operator refuses upgrades below 16")
		}
	}
}
