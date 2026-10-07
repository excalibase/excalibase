package domain

import (
	"fmt"
	"strings"
	"time"
)

// UTCTime is a moment written in UTC with its zone (RFC 3339), so a reader
// never has to guess the server's clock zone. It also reads the zoneless
// form FlexTime wrote, as UTC.
type UTCTime struct {
	time.Time
}

func (t UTCTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.Time.UTC().Format(time.RFC3339Nano) + `"`), nil
}

func (t *UTCTime) UnmarshalJSON(b []byte) error {
	raw := strings.Trim(string(b), `"`)
	if raw == "" || raw == "null" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			t.Time = parsed.UTC()
			return nil
		}
	}
	return fmt.Errorf("unreadable time %q", raw)
}
