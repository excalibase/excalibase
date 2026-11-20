package metrics

import (
	"context"
	"database/sql/driver"
	"errors"
)

// nopConnector is a driver that never connects: the stats collector only
// reads the pool's counters.
type nopConnector struct{}

func (nopConnector) Connect(context.Context) (driver.Conn, error) { return nil, errors.New("no db") }
func (nopConnector) Driver() driver.Driver                        { return nil }
