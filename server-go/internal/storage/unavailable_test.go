package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
)

func TestIsUnavailable(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":              {nil, false},
		"deadline":         {fmt.Errorf("find: %w", context.DeadlineExceeded), true},
		"bad conn":         {driver.ErrBadConn, true},
		"pool busy":        {ErrPoolBusy, true},
		"conn done":        {sql.ErrConnDone, true},
		"too many conns":   {&pq.Error{Code: "53300"}, true},
		"shutdown":         {&pq.Error{Code: "57P01"}, true},
		"connection error": {&pq.Error{Code: "08006"}, true},
		"unique violation": {&pq.Error{Code: "23505"}, false},
		"no rows":          {sql.ErrNoRows, false},
		"plain not found":  {errors.New("user not found"), false},
		"database closed":  {errors.New("sql: database is closed"), true},
	}
	for name, tc := range cases {
		if got := IsUnavailable(tc.err); got != tc.want {
			t.Errorf("%s: IsUnavailable = %v, want %v", name, got, tc.want)
		}
	}
}
