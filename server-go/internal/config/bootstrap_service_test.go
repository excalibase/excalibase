package config

import (
	"reflect"
	"testing"
)

func TestParseBootstrapServicePermissions(t *testing.T) {
	got := ParseBootstrapServicePermissions(" vault:init, service-tokens:manage:svc-auth ,,")
	want := []string{"vault:init", "service-tokens:manage:svc-auth"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := ParseBootstrapServicePermissions(""); len(got) != 0 {
		t.Fatalf("empty input: got %v", got)
	}
}
