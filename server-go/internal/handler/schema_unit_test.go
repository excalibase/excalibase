package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSafeError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"simple error", errors.New("something failed"), "something failed"},
		{"strips pq prefix", errors.New("pq: relation \"users\" does not exist"), `relation "users" does not exist`},
		{"strips ERROR prefix", errors.New("ERROR: syntax error at position 5"), "syntax error at position 5"},
		{"strips DETAIL line", errors.New("something\nDETAIL: key (id)=(1) already exists"), "something"},
		{"strips HINT line", errors.New("something\nHINT: try reindexing"), "something"},
		{"truncates to 200 chars", errors.New(string(make([]byte, 300))), string(make([]byte, 200))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeError(tt.err)
			if got != tt.expected {
				t.Errorf("safeError() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSchemaParam(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{"", "public"},                           // empty → default
		{"schema=public", "public"},              // valid
		{"schema=my_schema", "my_schema"},        // valid underscore
		{"schema=%27%3B+DROP+TABLE--", "public"}, // injection → default
		{"schema=1digit", "public"},              // starts with digit → default
		{"schema=has%20space", "public"},         // space → default
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/?"+tt.query, nil)
			got := schemaParam(r)
			if got != tt.expected {
				t.Errorf("schemaParam(%q) = %q, want %q", tt.query, got, tt.expected)
			}
		})
	}
}

func TestIndexOfByte(t *testing.T) {
	tests := []struct {
		s        string
		sep      byte
		expected int
	}{
		{"hello:world", ':', 5},
		{"nocolon", ':', -1},
		{":first", ':', 0},
		{"", ':', -1},
	}
	for _, tt := range tests {
		got := indexOfByte(tt.s, tt.sep)
		if got != tt.expected {
			t.Errorf("indexOfByte(%q, %c) = %d, want %d", tt.s, tt.sep, got, tt.expected)
		}
	}
}

func TestSchemaError(t *testing.T) {
	w := httptest.NewRecorder()
	schemaError(w, errors.New("pq: table not found\nDETAIL: schema info"), http.StatusInternalServerError)

	if w.Code != 500 {
		t.Errorf("expected 500, got %d", w.Code)
	}
	body := w.Body.String()
	if contains(body, "DETAIL") {
		t.Error("response should not contain DETAIL")
	}
	if contains(body, "pq:") {
		t.Error("response should not contain pq: prefix")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
