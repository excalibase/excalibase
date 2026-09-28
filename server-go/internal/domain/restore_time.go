package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrTargetTimeFormat refuses a point-in-time target that is not an RFC 3339
// timestamp with a zone. A zone-less time names a different instant in every
// zone, so it is never guessed.
var ErrTargetTimeFormat = errors.New("targetTime must be an RFC 3339 timestamp with a time zone, e.g. 2026-09-28T02:18:06.782Z")

// recoveryTimeLayout is CNPG's RFC3339Micro: Postgres keeps microseconds, so
// anything coarser moves the target before commits the caller asked for.
const recoveryTimeLayout = "2006-01-02T15:04:05.000000Z07:00"

// ZonedTime is an instant decoded only from a timestamp that says its zone.
type ZonedTime struct {
	time.Time
}

func (z *ZonedTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return ErrTargetTimeFormat
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("%w: got %q", ErrTargetTimeFormat, s)
	}
	z.Time = t
	return nil
}

func (z ZonedTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(z.Time.Format(time.RFC3339Nano))
}

// RecoveryTime is the instant as Postgres recovery reads it: UTC, microseconds.
func (z ZonedTime) RecoveryTime() string {
	return z.Time.UTC().Truncate(time.Microsecond).Format(recoveryTimeLayout)
}
