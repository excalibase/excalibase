package config

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"testing"
)

// Exposure enforcement is a product decision, not a per-project opt-in: every
// project is filtered to its grant list. The only way to turn it off is this
// one platform-wide setting, and it is on unless an operator says otherwise.
func TestExposureEnforcedDefaultsOn(t *testing.T) {
	os.Unsetenv(exposureEnvKey)
	if !Load().ExposureEnforced {
		t.Fatal("exposure enforcement must default ON for the whole installation")
	}
}

func TestExposureEnforcedIsPlatformWideKillSwitch(t *testing.T) {
	t.Setenv(exposureEnvKey, "false")
	if Load().ExposureEnforced {
		t.Fatal("EXCALIBASE_EXPOSURE_ENFORCED=false must turn enforcement off installation-wide")
	}
}

// A typo must stop the server, exactly as a malformed duration does. Silently
// falling back would leave an operator believing enforcement is off (or on)
// when it is the other way round.
func TestExposureEnforcedRefusesUnparseableValue(t *testing.T) {
	for _, raw := range []string{"maybe", "2", "", " "} {
		if _, err := parseStrictBool(raw); err == nil {
			t.Errorf("parseStrictBool(%q) must be an error, not a silent fallback", raw)
		}
	}
	for raw, want := range map[string]bool{
		"true": true, "TRUE": true, "1": true, "yes": true, "on": true,
		"false": false, "FALSE": false, "0": false, "no": false, "off": false,
	} {
		got, err := parseStrictBool(raw)
		if err != nil || got != want {
			t.Errorf("parseStrictBool(%q) = %v, %v; want %v, nil", raw, got, err, want)
		}
	}
}

// A mistyped kill switch must stop the process, not boot with a guess. That
// path ends in log.Fatalf, so it can only be observed from outside: the test
// re-runs itself as a child with the bad value set and checks the child died
// saying which setting it choked on.
func TestExposureEnforcedFatalsOnUnparseableValue(t *testing.T) {
	if os.Getenv("EXPOSURE_FATAL_CHILD") == "1" {
		Load()
		return // unreachable while Load fatals, which is what the parent asserts
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestExposureEnforcedFatalsOnUnparseableValue")
	cmd.Env = append(os.Environ(), "EXPOSURE_FATAL_CHILD=1", exposureEnvKey+"=maybe")
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("boot must fail on %s=maybe, got err=%v output=%q", exposureEnvKey, err, out)
	}
	if !bytes.Contains(out, []byte(exposureEnvKey)) || !bytes.Contains(out, []byte("not a boolean")) {
		t.Fatalf("the fatal message must name the setting and why it was refused, got: %q", out)
	}
}

// Turning the whole installation's exposure filter off must never be quiet.
func TestExposureDisabledLogsLoudlyAtBoot(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(os.Stderr)

	t.Setenv(exposureEnvKey, "false")
	Load()

	if !bytes.Contains(out.Bytes(), []byte(exposureEnvKey)) ||
		!bytes.Contains(out.Bytes(), []byte("EXPOSURE ENFORCEMENT IS OFF")) {
		t.Fatalf("boot log must shout that exposure enforcement is off, got: %q", out.String())
	}
}

func TestExposureEnabledDoesNotLog(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(os.Stderr)

	t.Setenv(exposureEnvKey, "true")
	Load()

	if bytes.Contains(out.Bytes(), []byte("EXPOSURE ENFORCEMENT IS OFF")) {
		t.Fatalf("no warning expected while enforcement is on, got: %q", out.String())
	}
}
