package config

import (
	"os"
	"reflect"
	"testing"
)

const (
	testCORSFmt = "parseCORSOrigins: got %v, want %v"
	testBCom    = "http://b.com"
	testACom    = "http://a.com"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.Port != "24005" {
		t.Errorf("port: got %s, want 24005", cfg.Port)
	}
	if cfg.StoragePath != "../provisioning-data" {
		t.Errorf("storagePath: got %s", cfg.StoragePath)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("logLevel: got %s", cfg.LogLevel)
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("PORT", "9999")
	os.Setenv("STORAGE_PATH", "/tmp/test-data")
	os.Setenv("LOG_LEVEL", "info")
	defer func() {
		os.Unsetenv("PORT")
		os.Unsetenv("STORAGE_PATH")
		os.Unsetenv("LOG_LEVEL")
	}()

	cfg := Load()
	if cfg.Port != "9999" {
		t.Errorf("port: got %s, want 9999", cfg.Port)
	}
	if cfg.StoragePath != "/tmp/test-data" {
		t.Errorf("storagePath: got %s", cfg.StoragePath)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("logLevel: got %s", cfg.LogLevel)
	}
}

func TestLoadDenoDefaults(t *testing.T) {
	cfg := Load()
	if cfg.DenoRuntimeURL != "http://deno-runtime.serverless.svc.cluster.local:8000" {
		t.Errorf("DenoRuntimeURL: got %s", cfg.DenoRuntimeURL)
	}
	if cfg.DenoNamespace != "serverless" {
		t.Errorf("DenoNamespace: got %s", cfg.DenoNamespace)
	}
	if cfg.DenoRuntimeImage != "excalibase/deno-runtime:latest" {
		t.Errorf("DenoRuntimeImage: got %s", cfg.DenoRuntimeImage)
	}
	if cfg.DenoRuntimeSecret != "" {
		t.Errorf("DenoRuntimeSecret: expected empty default, got %s", cfg.DenoRuntimeSecret)
	}
}

func TestLoadDenoFromEnv(t *testing.T) {
	os.Setenv("DENO_RUNTIME_URL", "http://custom-deno:9000")
	os.Setenv("DENO_RUNTIME_SECRET", "mysecret")
	os.Setenv("DENO_NAMESPACE", "custom-ns")
	os.Setenv("DENO_RUNTIME_IMAGE", "my-org/deno:v2")
	defer func() {
		os.Unsetenv("DENO_RUNTIME_URL")
		os.Unsetenv("DENO_RUNTIME_SECRET")
		os.Unsetenv("DENO_NAMESPACE")
		os.Unsetenv("DENO_RUNTIME_IMAGE")
	}()

	cfg := Load()
	if cfg.DenoRuntimeURL != "http://custom-deno:9000" {
		t.Errorf("DenoRuntimeURL: got %s", cfg.DenoRuntimeURL)
	}
	if cfg.DenoRuntimeSecret != "mysecret" {
		t.Errorf("DenoRuntimeSecret: got %s", cfg.DenoRuntimeSecret)
	}
	if cfg.DenoNamespace != "custom-ns" {
		t.Errorf("DenoNamespace: got %s", cfg.DenoNamespace)
	}
	if cfg.DenoRuntimeImage != "my-org/deno:v2" {
		t.Errorf("DenoRuntimeImage: got %s", cfg.DenoRuntimeImage)
	}
}

// --- parseCORSOrigins ---

// TestParseCORSOriginsEmpty and TestParseCORSOriginsAllEmpty removed:
// parseCORSOrigins now calls log.Fatal on empty/all-empty input.
// The Load() function always provides a non-empty default.

func TestParseCORSOriginsWildcard(t *testing.T) {
	got := parseCORSOrigins("*")
	want := []string{"*"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseCORSOrigins(%q) = %v, want %v", "*", got, want)
	}
}

func TestParseCORSOriginsSingle(t *testing.T) {
	got := parseCORSOrigins("http://localhost:3000")
	want := []string{"http://localhost:3000"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf(testCORSFmt, got, want)
	}
}

func TestParseCORSOriginsMultiple(t *testing.T) {
	got := parseCORSOrigins("http://a.com,http://b.com")
	want := []string{testACom, testBCom}
	if !reflect.DeepEqual(got, want) {
		t.Errorf(testCORSFmt, got, want)
	}
}

func TestParseCORSOriginsTrimsSpaces(t *testing.T) {
	got := parseCORSOrigins("http://a.com, http://b.com ")
	want := []string{testACom, testBCom}
	if !reflect.DeepEqual(got, want) {
		t.Errorf(testCORSFmt, got, want)
	}
}

func TestParseCORSOriginsSkipsEmptyParts(t *testing.T) {
	got := parseCORSOrigins("http://a.com,,http://b.com")
	want := []string{testACom, testBCom}
	if !reflect.DeepEqual(got, want) {
		t.Errorf(testCORSFmt, got, want)
	}
}

// --- CORS_ORIGINS env var integration ---

func TestLoadCORSOriginsFromEnv(t *testing.T) {
	os.Setenv("CORS_ORIGINS", "http://app.example.com,http://admin.example.com")
	defer os.Unsetenv("CORS_ORIGINS")

	cfg := Load()
	want := []string{"http://app.example.com", "http://admin.example.com"}
	if !reflect.DeepEqual(cfg.CORSOrigins, want) {
		t.Errorf("CORSOrigins: got %v, want %v", cfg.CORSOrigins, want)
	}
}

func TestLoadCORSOriginsDefaultsToAppOrigin(t *testing.T) {
	os.Unsetenv("CORS_ORIGINS")
	cfg := Load()
	if len(cfg.CORSOrigins) != 1 || cfg.CORSOrigins[0] != "https://app.excalibase.io" {
		t.Errorf("CORSOrigins default: got %v, want [https://app.excalibase.io]", cfg.CORSOrigins)
	}
}

func TestLoadBYOCEgressAllowlist(t *testing.T) {
	if got := Load().BYOCEgressAllowlist; got != "" {
		t.Errorf("BYOCEgressAllowlist default: got %q, want empty", got)
	}
	t.Setenv("BYOC_EGRESS_ALLOWLIST", "203.0.113.0/24, *.rds.amazonaws.com")
	if got := Load().BYOCEgressAllowlist; got != "203.0.113.0/24, *.rds.amazonaws.com" {
		t.Errorf("BYOCEgressAllowlist from env: got %q", got)
	}
}
