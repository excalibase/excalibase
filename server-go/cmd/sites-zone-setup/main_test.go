package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestRunNeedsAToken(t *testing.T) {
	err := run(context.Background(), []string{"--zone-id", "0123456789abcdef0123456789abcdef", "--base-domain", "excalibase.app"}, env(nil), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRefusesAZoneNameForItsID(t *testing.T) {
	err := run(context.Background(), []string{"--zone-id", "excalibase.app", "--base-domain", "excalibase.app"},
		env(map[string]string{"CLOUDFLARE_API_TOKEN": "t"}), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "zone id") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRefusesAnOriginOutsideTheBaseDomain(t *testing.T) {
	err := run(context.Background(), []string{"--zone-id", "0123456789abcdef0123456789abcdef", "--base-domain", "excalibase.app", "--origin", "x.example.com"},
		env(map[string]string{"CLOUDFLARE_API_TOKEN": "t"}), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "under") {
		t.Fatalf("err = %v", err)
	}
}
