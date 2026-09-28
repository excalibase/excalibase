package handler

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/lib/pq"
)

func TestRoleErrorStatus(t *testing.T) {
	cases := map[error]int{
		fmt.Errorf("x: %w", schema.ErrProtectedRole):            http.StatusForbidden,
		fmt.Errorf("create role: %w", &pq.Error{Code: "42501"}): http.StatusForbidden,
		fmt.Errorf("create role: %w", &pq.Error{Code: "42939"}): http.StatusForbidden,
		fmt.Errorf("create role: %w", &pq.Error{Code: "42710"}): http.StatusConflict,
		fmt.Errorf("create role: %w", &pq.Error{Code: "42602"}): http.StatusBadRequest,
		fmt.Errorf("drop role: %w", &pq.Error{Code: "2BP01"}):   http.StatusConflict,
		errors.New("connection refused"):                        http.StatusInternalServerError,
	}
	for err, want := range cases {
		if got := roleErrorStatus(err); got != want {
			t.Errorf("%v: %d, want %d", err, got, want)
		}
	}
}
