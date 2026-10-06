package schema

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lib/pq"
)

func TestReadOnlyErrorNamesTheRefusedWrite(t *testing.T) {
	refused := fmt.Errorf("query: %w", &pq.Error{Code: "25006", Message: "cannot execute SELECT in a read-only transaction"})
	if got := readOnlyError(refused); !strings.Contains(got, "read-only SQL cannot change data or schema") || strings.Contains(got, "SELECT") {
		t.Errorf("got %q", got)
	}
	syntax := &pq.Error{Code: "42601", Message: "syntax error at or near \"selec\""}
	if got := readOnlyError(syntax); got != syntax.Error() {
		t.Errorf("other errors pass through, got %q", got)
	}
	if got := readOnlyError(errors.New("connection reset")); got != "connection reset" {
		t.Errorf("got %q", got)
	}
}
