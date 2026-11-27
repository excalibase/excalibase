package adminconsole

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAttentionPage = 20
	MaxAttentionPage     = 50
	suspendedWindow      = 24 * time.Hour
)

// Cursor is the place a keyset page starts after: the sort time of the last row
// and its id, which breaks ties. At is nil for a row with no time (it sorts last).
type Cursor struct {
	At *time.Time
	ID string
}

// EncodeCursor makes the opaque token a client hands back for the next page.
func EncodeCursor(cursor Cursor) string {
	stamp := ""
	if cursor.At != nil {
		stamp = strconv.FormatInt(cursor.At.UnixMicro(), 10)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(stamp + "|" + cursor.ID))
}

// DecodeCursor refuses anything EncodeCursor did not make, so a damaged or
// invented cursor is a 400 rather than a page from the wrong place.
func DecodeCursor(token string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	stamp, id, found := strings.Cut(string(raw), "|")
	if err != nil || !found || id == "" {
		return Cursor{}, ErrInvalidQuery
	}
	if stamp == "" {
		return Cursor{ID: id}, nil
	}
	micros, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return Cursor{}, ErrInvalidQuery
	}
	at := time.UnixMicro(micros).UTC()
	return Cursor{At: &at, ID: id}, nil
}

// CursorPage is one page of a list read by keyset, the total it belongs to and
// where the next page starts ("" on the last).
type CursorPage[T any] struct {
	Items      []T    `json:"items"`
	Total      int    `json:"total"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// AttentionQuery asks for Limit rows after a cursor. Since bounds the suspended list.
type AttentionQuery struct {
	Since time.Time
	After *Cursor
	Limit int
}

// AttentionReader reads the lists of the "needs attention" card a page at a time.
type AttentionReader interface {
	ListSuspended(ctx context.Context, query AttentionQuery) (CursorPage[SuspendedBrief], error)
	ListFailedProjects(ctx context.Context, query AttentionQuery) (CursorPage[ProjectBrief], error)
}

// Attention serves the card's lists: validated cursor, capped page size.
type Attention struct {
	reader AttentionReader
	now    func() time.Time
}

func NewAttention(reader AttentionReader) *Attention {
	return &Attention{reader: reader, now: time.Now}
}

func (a *Attention) query(token string, limit int) (AttentionQuery, error) {
	query := AttentionQuery{Since: a.now().Add(-suspendedWindow), Limit: limit}
	if query.Limit <= 0 {
		query.Limit = DefaultAttentionPage
	}
	query.Limit = min(query.Limit, MaxAttentionPage)
	if token != "" {
		cursor, err := DecodeCursor(token)
		if err != nil {
			return AttentionQuery{}, err
		}
		query.After = &cursor
	}
	return query, nil
}

// Suspensions pages the accounts suspended in the last day, newest first.
func (a *Attention) Suspensions(ctx context.Context, token string, limit int) (CursorPage[SuspendedBrief], error) {
	query, err := a.query(token, limit)
	if err != nil {
		return CursorPage[SuspendedBrief]{}, err
	}
	page, err := a.reader.ListSuspended(ctx, query)
	if err != nil {
		return CursorPage[SuspendedBrief]{}, fmt.Errorf("list suspended: %w", err)
	}
	return page, nil
}

// FailedProjects pages the projects that failed, most recently changed first.
func (a *Attention) FailedProjects(ctx context.Context, token string, limit int) (CursorPage[ProjectBrief], error) {
	query, err := a.query(token, limit)
	if err != nil {
		return CursorPage[ProjectBrief]{}, err
	}
	page, err := a.reader.ListFailedProjects(ctx, query)
	if err != nil {
		return CursorPage[ProjectBrief]{}, fmt.Errorf("list failed projects: %w", err)
	}
	return page, nil
}
