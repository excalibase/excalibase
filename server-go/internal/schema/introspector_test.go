package schema

import (
	"testing"
)

func TestParseIndexType(t *testing.T) {
	tests := []struct {
		def  string
		want string
	}{
		{"CREATE INDEX idx ON t USING btree (id)", "btree"},
		{"CREATE INDEX idx ON t USING hash (id)", "hash"},
		{"CREATE INDEX idx ON t USING gin (tags)", "gin"},
		{"CREATE INDEX idx ON t USING gist (location)", "gist"},
		{"CREATE INDEX idx ON t USING brin (created_at)", "brin"},
		{"CREATE UNIQUE INDEX idx ON t (id)", "btree"},
		{"", "btree"},
	}
	for _, tt := range tests {
		got := parseIndexType(tt.def)
		if got != tt.want {
			t.Errorf("parseIndexType(%q) = %q, want %q", tt.def, got, tt.want)
		}
	}
}

func TestParseIndexColumns(t *testing.T) {
	tests := []struct {
		def  string
		want []string
	}{
		{"CREATE INDEX idx ON t (id)", []string{"id"}},
		{"CREATE INDEX idx ON t (first_name, last_name)", []string{"first_name", "last_name"}},
		{"CREATE UNIQUE INDEX idx ON t USING btree (email)", []string{"email"}},
		{"", []string{}},
		{"no parens", []string{}},
	}
	for _, tt := range tests {
		got := parseIndexColumns(tt.def)
		if len(got) != len(tt.want) {
			t.Errorf("parseIndexColumns(%q) len = %d, want %d", tt.def, len(got), len(tt.want))
			continue
		}
		for i, col := range got {
			if col != tt.want[i] {
				t.Errorf("parseIndexColumns(%q)[%d] = %q, want %q", tt.def, i, col, tt.want[i])
			}
		}
	}
}
