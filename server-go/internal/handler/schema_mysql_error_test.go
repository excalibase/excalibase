package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// EXC-619: a MySQL refusal reads like a Postgres one: its own message, and a
// status that says whose mistake it was.
func TestSchemaError_AnswersAMySQLRefusalWithItsMessage(t *testing.T) {
	for number, want := range map[uint16]int{
		1050: http.StatusConflict,   // table exists
		1062: http.StatusConflict,   // duplicate entry
		1064: http.StatusBadRequest, // syntax
		1146: http.StatusBadRequest, // no such table
		1142: http.StatusForbidden,  // command denied
		1205: http.StatusInternalServerError,
	} {
		refusal := &mysql.MySQLError{Number: number, Message: fmt.Sprintf("refusal %d", number)}
		w := httptest.NewRecorder()
		schemaError(w, fmt.Errorf("create table: %w", refusal), http.StatusInternalServerError)
		if w.Code != want || !strings.Contains(w.Body.String(), fmt.Sprintf("refusal %d", number)) {
			t.Errorf("%d: %d %s", number, w.Code, w.Body.String())
		}
	}
}
