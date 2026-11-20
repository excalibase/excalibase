package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/lib/pq"
)

// ErrPoolBusy is returned when the platform database pool had no free
// connection within the wait budget. The database may be perfectly healthy;
// callers must treat it as "try again", never as a verdict on the request.
var ErrPoolBusy = errors.New("platform database pool is busy")

// IsUnavailable reports whether err means the platform database could not
// answer (timeout, lost or refused connection, exhausted pool, server
// shutting down) as opposed to answering "no such row" or rejecting the
// statement. Auth uses it to answer 503 instead of 401.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPoolBusy) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, sql.ErrConnDone) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var pgErr *pq.Error
	if errors.As(err, &pgErr) {
		// 08 connection exception, 53 insufficient resources (too many
		// connections), 57 operator intervention (shutdown, cancel).
		class := string(pgErr.Code.Class())
		return class == "08" || class == "53" || class == "57"
	}
	return strings.Contains(err.Error(), "sql: database is closed")
}
