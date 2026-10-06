package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/go-chi/chi/v5"
)

// An invalid ?schema= is refused, never replaced by public: a DROP aimed at
// another schema must not land on public.<table>.
func TestSchemaRoutesRefuseAnInvalidSchemaParam(t *testing.T) {
	h := &SchemaHandler{}
	r := chi.NewRouter()
	h.Routes(r)
	for _, target := range []string{
		"/p1/tables/orders?schema=Bad%20Schema",
		"/p1/tables/orders?schema=%27%3BDROP",
	} {
		req := httptest.NewRequest(http.MethodDelete, target, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, w.Code)
		}
	}
}

func TestRequireValidSchemaParamPassesAValidOrAbsentSchema(t *testing.T) {
	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ })
	for _, target := range []string{"/", "/?schema=public", "/?schema=my_schema"} {
		w := httptest.NewRecorder()
		requireValidSchemaParam(next).ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	}
	if reached != 3 {
		t.Errorf("reached = %d, want 3", reached)
	}
}

func TestSchemaErrorRefusesOurOwnValidationWith400(t *testing.T) {
	w := httptest.NewRecorder()
	schemaError(w, fmt.Errorf("column %q: %w", "a", schema.ValidateTypeName("text; drop")), http.StatusInternalServerError)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid type name") {
		t.Errorf("body = %s", w.Body.String())
	}
}

// A bigint past 2^53 and a long numeric reach the database as written.
func TestDecodeRowDataKeepsNumbersExact(t *testing.T) {
	body := `{"data":{"id":9007199254740993,"price":12.345678901234567890,"doc":{"a":1}}}`
	data, err := decodeRowData(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if data["id"] != json.Number("9007199254740993") {
		t.Errorf("id = %#v", data["id"])
	}
	if data["price"] != json.Number("12.345678901234567890") {
		t.Errorf("price = %#v", data["price"])
	}
}

func TestDecodeRowDataAcceptsAnEmptyRow(t *testing.T) {
	data, err := decodeRowData(strings.NewReader(`{"data":{}}`))
	if err != nil || len(data) != 0 {
		t.Fatalf("data = %v err = %v", data, err)
	}
	if _, err := decodeRowData(strings.NewReader(`not json`)); err == nil {
		t.Error("invalid JSON was accepted")
	}
}
