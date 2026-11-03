package logsafe

import "testing"

func TestValueStripsLineTerminators(t *testing.T) {
	got := Value("proj\nINFO forged\r\nentry")
	if got != "projINFO forgedentry" {
		t.Fatalf("Value kept a line terminator: %q", got)
	}
}

func TestValueKeepsOrdinaryText(t *testing.T) {
	if got := Value("/api/projects/p1/functions"); got != "/api/projects/p1/functions" {
		t.Fatalf("Value changed a plain value: %q", got)
	}
}
