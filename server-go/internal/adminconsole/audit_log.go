package adminconsole

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAuditPage = 25
	MaxAuditPage     = 100
	// MaxAuditFilterLength bounds each filter, so a request cannot make the query carry a megabyte of text.
	MaxAuditFilterLength = 200
	// MaxAuditDetails cuts a row's details short: the list is for finding an entry, and the full text of a
	// tenant's request does not belong in a page that spans every tenant.
	MaxAuditDetails = 300
)

// AuditRow is one entry of the platform audit log. Actor and target carry the
// account's address when the id belongs to a user.
type AuditRow struct {
	ID          string     `json:"id"`
	At          *time.Time `json:"at,omitempty"`
	ActorID     string     `json:"actorId,omitempty"`
	ActorEmail  string     `json:"actorEmail,omitempty"`
	Action      string     `json:"action"`
	Resource    string     `json:"resource,omitempty"`
	ResourceID  string     `json:"resourceId,omitempty"`
	TargetEmail string     `json:"targetEmail,omitempty"`
	Details     string     `json:"details,omitempty"`
	IP          string     `json:"ip,omitempty"`
}

// AuditFilter narrows the log. Actor is a user id or an address, Action a
// prefix of the action name ("user." for every account action) and Target the
// id of what was acted on.
type AuditFilter struct {
	Actor, Action, Target string
}

// AuditQuery is a filter, the row id the page starts below (0 for the newest) and a page size.
type AuditQuery struct {
	Actor, Action, Target string
	BeforeID              int64
	Limit                 int
}

// AuditPage is one page, newest first. NextCursor is empty on the last page.
// The log has no total: counting a table this large on every page costs more than it tells.
type AuditPage struct {
	Items      []AuditRow `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
	// More is set by the reader when rows remain past Items.
	More bool `json:"-"`
}

// AuditReader reads the audit log, newest first, at most Limit rows.
type AuditReader interface {
	ListAudit(ctx context.Context, query AuditQuery) (AuditPage, error)
}

// AuditLog serves the log: validated filters and cursor, capped page size.
type AuditLog struct{ reader AuditReader }

func NewAuditLog(reader AuditReader) *AuditLog { return &AuditLog{reader: reader} }

// List returns the page after token (empty for the first page).
func (l *AuditLog) List(ctx context.Context, filter AuditFilter, token string, limit int) (AuditPage, error) {
	query := AuditQuery{
		Actor: strings.TrimSpace(filter.Actor), Action: strings.TrimSpace(filter.Action), Target: strings.TrimSpace(filter.Target),
		Limit: limit,
	}
	for _, value := range []string{query.Actor, query.Action, query.Target} {
		if len(value) > MaxAuditFilterLength {
			return AuditPage{}, ErrInvalidQuery
		}
	}
	if query.Limit <= 0 {
		query.Limit = DefaultAuditPage
	}
	query.Limit = min(query.Limit, MaxAuditPage)
	if token != "" {
		before, err := auditCursorID(token)
		if err != nil {
			return AuditPage{}, err
		}
		query.BeforeID = before
	}
	page, err := l.reader.ListAudit(ctx, query)
	if err != nil {
		return AuditPage{}, fmt.Errorf("list audit: %w", err)
	}
	if page.Items == nil {
		page.Items = []AuditRow{}
	}
	for i := range page.Items {
		page.Items[i].Details = ShortDetails(page.Items[i].Details)
	}
	page.NextCursor = ""
	if page.More && len(page.Items) > 0 {
		page.NextCursor = EncodeCursor(Cursor{ID: page.Items[len(page.Items)-1].ID})
	}
	return page, nil
}

// ShortDetails cuts an entry's details to MaxAuditDetails characters.
func ShortDetails(details string) string {
	runes := []rune(details)
	if len(runes) <= MaxAuditDetails {
		return details
	}
	return string(runes[:MaxAuditDetails]) + "…"
}

func auditCursorID(token string) (int64, error) {
	cursor, err := DecodeCursor(token)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(cursor.ID, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrInvalidQuery
	}
	return id, nil
}
