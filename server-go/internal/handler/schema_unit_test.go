package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/lib/pq"
)

// An over-long table or column name is the caller's mistake, said plainly.
func TestSchemaErrorRefusesAnInvalidNameWith400(t *testing.T) {
	w := httptest.NewRecorder()
	schemaError(w, fmt.Errorf("create table: %w", schema.CheckIdentifier("table", strings.Repeat("a", 300))), http.StatusInternalServerError)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "Table names can be at most 63 characters; this one has 300" {
		t.Errorf("error = %q", body.Error)
	}
}

// A Postgres refusal reaches the user as Postgres's own sentence, without the
// wrapping our code adds, and with a status that says whose mistake it was.
func TestSchemaErrorPostgresRefusals(t *testing.T) {
	wrap := func(code pq.ErrorCode, msg string) error {
		return fmt.Errorf("create table: %w", &pq.Error{Code: code, Message: msg, Detail: "Key (id)=(1)"})
	}
	cases := []struct {
		name string
		err  error
		want int
		msg  string
	}{
		{"duplicate table", wrap("42P07", `relation "todos" already exists`), http.StatusConflict, `relation "todos" already exists`},
		{"duplicate column", wrap("42701", `column "title" specified more than once`), http.StatusConflict, `column "title" specified more than once`},
		{"duplicate object", wrap("42710", `policy "p" for table "t" already exists`), http.StatusConflict, `policy "p" for table "t" already exists`},
		{"unique violation", wrap("23505", `duplicate key value violates unique constraint "todos_pkey"`), http.StatusConflict, `duplicate key value violates unique constraint "todos_pkey"`},
		{"unknown type", wrap("42704", `type "moneyz" does not exist`), http.StatusBadRequest, `type "moneyz" does not exist`},
		{"syntax", wrap("42601", `syntax error at or near "frm"`), http.StatusBadRequest, `syntax error at or near "frm"`},
		{"bad value", wrap("22P02", `invalid input syntax for type integer: "abc"`), http.StatusBadRequest, `invalid input syntax for type integer: "abc"`},
		{"not null", wrap("23502", `null value in column "title" violates not-null constraint`), http.StatusBadRequest, `null value in column "title" violates not-null constraint`},
		{"no privilege", wrap("42501", `permission denied for table secrets`), http.StatusForbidden, `permission denied for table secrets`},
		{"server side", wrap("53300", `too many connections`), http.StatusInternalServerError, `too many connections`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			schemaError(w, tc.err, http.StatusInternalServerError)
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error != tc.msg {
				t.Errorf("error = %q, want %q", body.Error, tc.msg)
			}
		})
	}
}

func TestSchemaErrorKeepsAnExplicitStatus(t *testing.T) {
	w := httptest.NewRecorder()
	schemaError(w, &pq.Error{Code: "42P07", Message: "x"}, http.StatusForbidden)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want the caller's 403", w.Code)
	}
}

func TestSafeErrorStripsAWrappedDriverPrefix(t *testing.T) {
	if got := safeError(errors.New(`create table: pq: relation "x" already exists`)); got != `create table: relation "x" already exists` {
		t.Errorf("safeError = %q", got)
	}
}

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
		{"", "public"},                    // empty → default
		{"schema=public", "public"},       // valid
		{"schema=my_schema", "my_schema"}, // valid underscore
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
