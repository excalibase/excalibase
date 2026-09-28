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

func TestParseServiceTokenCeilings(t *testing.T) {
	got, err := ParseServiceTokenCeilings(`{"svc-auth":["email:send"],"svc-graphql":["policies:read"]}`)
	if err != nil || !reflect.DeepEqual(got["svc-auth"], []string{"email:send"}) || len(got) != 2 {
		t.Fatalf("got %v err %v", got, err)
	}
	if got, err := ParseServiceTokenCeilings(""); err != nil || len(got) != 0 {
		t.Fatalf("unset: %v %v", got, err)
	}
	if _, err := ParseServiceTokenCeilings("svc-auth=email:send"); err == nil {
		t.Fatal("malformed value must be refused, not read as no ceilings")
	}
}
