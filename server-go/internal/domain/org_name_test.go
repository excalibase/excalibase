package domain

import (
	"strings"
	"testing"
)

func TestNormalizeOrgName(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Acme", "Acme", true},
		{"  Acme Inc  ", "Acme Inc", true},
		{"", "", false},
		{"   ", "", false},
		{strings.Repeat("a", MaxOrgNameLength), strings.Repeat("a", MaxOrgNameLength), true},
		{strings.Repeat("a", MaxOrgNameLength+1), "", false},
		// Counted in characters, not bytes.
		{strings.Repeat("é", MaxOrgNameLength), strings.Repeat("é", MaxOrgNameLength), true},
	}
	for _, c := range cases {
		got, ok := NormalizeOrgName(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("NormalizeOrgName(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
