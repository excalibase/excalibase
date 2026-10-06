package config

import (
	"errors"
	"testing"
)

func standardTier() TierConfig {
	return TierConfig{Instances: 1, StorageSize: "50Gi", Memory: "4Gi", CPU: "2", StatementTimeout: "30s"}
}

func TestPlatformOwnedParametersAreRefused(t *testing.T) {
	for _, name := range []string{
		"statement_timeout", "idle_in_transaction_session_timeout", "max_connections",
		"shared_preload_libraries", "shared_buffers", "log_statement", "ssl_ciphers",
		"Statement_Timeout", "cron.database_name", "archive_command",
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateTenantParameters(map[string]string{name: "0"}, standardTier())
			if !errors.Is(err, ErrTenantParameter) {
				t.Fatalf("%s: got %v, want ErrTenantParameter", name, err)
			}
		})
	}
}

func TestTunableParametersWithinBoundsAreAccepted(t *testing.T) {
	params := map[string]string{
		"work_mem":                      "64MB",
		"maintenance_work_mem":          "1GB",
		"effective_cache_size":          "3GB",
		"random_page_cost":              "1.1",
		"seq_page_cost":                 "1",
		"effective_io_concurrency":      "200",
		"default_statistics_target":     "500",
		"jit":                           "off",
		"default_transaction_isolation": "repeatable read",
		"lock_timeout":                  "5s",
		"deadlock_timeout":              "500ms",
	}
	if err := ValidateTenantParameters(params, standardTier()); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestNoParametersIsValid(t *testing.T) {
	if err := ValidateTenantParameters(nil, standardTier()); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestTunableParametersOutOfBoundsAreRefused(t *testing.T) {
	cases := map[string]string{
		"work_mem":                      "1GB",
		"maintenance_work_mem":          "2GB",
		"effective_cache_size":          "8GB",
		"random_page_cost":              "0",
		"seq_page_cost":                 "1e9",
		"effective_io_concurrency":      "5000",
		"default_statistics_target":     "0",
		"jit":                           "maybe",
		"default_transaction_isolation": "chaos",
		"lock_timeout":                  "2h",
		"deadlock_timeout":              "0ms",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateTenantParameters(map[string]string{name: value}, standardTier())
			if !errors.Is(err, ErrTenantParameter) {
				t.Fatalf("%s=%s: got %v, want ErrTenantParameter", name, value, err)
			}
		})
	}
}

func TestMalformedValuesAreRefused(t *testing.T) {
	cases := map[string]string{
		"work_mem":                  "64",
		"lock_timeout":              "5",
		"random_page_cost":          "fast",
		"seq_page_cost":             "NaN",
		"deadlock_timeout":          "1s\nlog_statement=all",
		"effective_cache_size":      "1  GB",
		"maintenance_work_mem":      "1gb",
		"default_statistics_target": "1.5",
		"jit":                       "maybe",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateTenantParameters(map[string]string{name: value}, standardTier())
			if !errors.Is(err, ErrTenantParameter) {
				t.Fatalf("%s=%q: got %v, want ErrTenantParameter", name, value, err)
			}
		})
	}
}

func TestMemoryBoundsFollowTheTier(t *testing.T) {
	free := TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"}
	if err := ValidateTenantParameters(map[string]string{"work_mem": "64MB"}, free); !errors.Is(err, ErrTenantParameter) {
		t.Fatalf("64MB work_mem on a 512Mi tier: got %v", err)
	}
	if err := ValidateTenantParameters(map[string]string{"work_mem": "32MB"}, free); err != nil {
		t.Fatalf("32MB work_mem on a 512Mi tier: got %v", err)
	}
}

func TestAMemoryParameterNeedsTheTiersMemory(t *testing.T) {
	err := ValidateTenantParameters(map[string]string{"work_mem": "4MB"}, TierConfig{})
	if !errors.Is(err, ErrTenantParameter) {
		t.Fatalf("got %v, want ErrTenantParameter", err)
	}
}

// Values are read the way Postgres reads them: surrounding space is dropped,
// a space may sit before the unit, and enum values ignore case. What is
// stored is the canonical spelling.
func TestParameterValuesAreReadAsPostgresReadsThem(t *testing.T) {
	given := map[string]string{
		"work_mem":                      " 64 MB ",
		"effective_cache_size":          "1GB\n",
		"lock_timeout":                  "5 s",
		"random_page_cost":              " 1.1",
		"default_statistics_target":     "500 ",
		"jit":                           "OFF",
		"default_transaction_isolation": "  Repeatable   READ ",
	}
	if err := ValidateTenantParameters(given, standardTier()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	want := map[string]string{
		"work_mem":                      "64MB",
		"effective_cache_size":          "1GB",
		"lock_timeout":                  "5s",
		"random_page_cost":              "1.1",
		"default_statistics_target":     "500",
		"jit":                           "off",
		"default_transaction_isolation": "repeatable read",
	}
	got := NormalizeTenantParameters(given)
	for name, value := range want {
		if got[name] != value {
			t.Errorf("%s: normalised %q, want %q", name, got[name], value)
		}
	}
	if given["jit"] != "OFF" {
		t.Error("normalising must not change the caller's map")
	}
	if NormalizeTenantParameters(nil) != nil {
		t.Error("no parameters normalise to none")
	}
	if got := NormalizeTenantParameters(map[string]string{"shared_buffers": " 1GB "}); got["shared_buffers"] != " 1GB " {
		t.Errorf("a parameter the tenant may not set is left for validation to refuse, got %q", got["shared_buffers"])
	}
}

func TestTenantTunableParameter(t *testing.T) {
	if !TenantTunableParameter("work_mem") {
		t.Error("work_mem should be tenant-tunable")
	}
	if TenantTunableParameter("statement_timeout") {
		t.Error("statement_timeout must not be tenant-tunable")
	}
}
