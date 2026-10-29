package main

import (
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-673: a single host's new Postgres projects carry statistics while slowqueries is on.
func TestSingleHostPostgresCarriesQueryStatsWithTheFeature(t *testing.T) {
	for _, on := range []bool{false, true} {
		cfg := config.AppConfig{ProvisionerMode: "docker"}
		if on {
			cfg.DarkFeatures = []features.Feature{features.SlowQueries}
		}
		if got := newDockerPostgres(cfg, nil).QueryStatsOn(); got != on {
			t.Errorf("slowqueries %v: query stats = %v", on, got)
		}
	}
}

// A copy restored on a single host while slowqueries is on carries statistics too.
func TestSingleHostRestoresCarryQueryStatsWithTheFeature(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(body), "dockerAdapter.SetQueryStats(queryStatsOn(cfg))") {
		t.Error("the single-host backup adapter never learns the slowqueries feature")
	}
}
