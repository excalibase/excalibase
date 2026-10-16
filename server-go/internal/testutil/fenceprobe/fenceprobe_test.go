package fenceprobe

import (
	"strings"
	"testing"
)

const sampleLog = `FENCEPROBE begin epoch=1792187324
FENCEPROBE 0.00 metadata OPEN
FENCEPROBE 0.00 dns BLOCKED
unrelated line
FENCEPROBE 1.21 metadata BLOCKED
FENCEPROBE 1.21 dns OPEN
FENCEPROBE end
`

func TestParseReadsOnlyProbeSamples(t *testing.T) {
	samples, err := Parse(sampleLog)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 4 || samples[0] != (Sample{At: 0, Name: "metadata", State: Open}) {
		t.Fatalf("samples = %+v", samples)
	}
}

func TestParseRefusesALogWithoutSamples(t *testing.T) {
	if _, err := Parse("FENCEPROBE begin epoch=1\nFENCEPROBE end\n"); err == nil {
		t.Fatal("want an error for a log with no samples")
	}
}

func TestParseRefusesAnUnknownState(t *testing.T) {
	if _, err := Parse("FENCEPROBE 0.10 dns MAYBE\n"); err == nil {
		t.Fatal("want an error for an unknown state")
	}
}

func TestParseRefusesABadTime(t *testing.T) {
	if _, err := Parse("FENCEPROBE soon dns OPEN\n"); err == nil {
		t.Fatal("want an error for a time that is not a number")
	}
}

func TestBreachesNameEachFencedTargetThatAnswered(t *testing.T) {
	samples, err := Parse(sampleLog)
	if err != nil {
		t.Fatal(err)
	}
	targets := []Target{{Name: "metadata", Addr: "169.254.169.254:80", Fenced: true}, {Name: "dns", Addr: "10.43.0.10:53"}}
	got := Breaches(samples, targets, 0)
	if len(got) != 1 || got[0] != "metadata (169.254.169.254:80) answered at t=0.00s" {
		t.Fatalf("breaches = %v", got)
	}
	if after := Breaches(samples, targets, 1); len(after) != 0 {
		t.Fatalf("breaches from t=1 = %v, want none", after)
	}
}

func TestFirstOpen(t *testing.T) {
	samples, err := Parse(sampleLog)
	if err != nil {
		t.Fatal(err)
	}
	if at := FirstOpen(samples, "dns", 0); at != 1.21 {
		t.Fatalf("dns first open = %v, want 1.21", at)
	}
	if at := FirstOpen(samples, "metadata", 0.5); at != -1 {
		t.Fatalf("metadata open after 0.5 = %v, want -1", at)
	}
}

func TestTargetsValueAndTimeline(t *testing.T) {
	value := TargetsValue([]Target{{Name: "a", Addr: "10.0.0.1:80"}, {Name: "b", Addr: "10.0.0.2:443"}})
	if value != "a=10.0.0.1:80 b=10.0.0.2:443" {
		t.Fatalf("targets value = %q", value)
	}
	samples, err := Parse(sampleLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(Timeline(samples), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "dns=BLOCKED metadata=OPEN") {
		t.Fatalf("timeline = %q", lines)
	}
}

// An app's arguments may not hold a newline, so the script must be one line.
func TestScriptIsOneLine(t *testing.T) {
	if strings.ContainsAny(Script, "\n\r") {
		t.Fatal("the script holds a newline")
	}
}
