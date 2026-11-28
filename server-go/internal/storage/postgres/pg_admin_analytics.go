package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	analyticsDomainLimit = 10
	analyticsLatestLimit = 20
	activeWindow         = 7 * 24 * time.Hour
	dayLayout            = "2006-01-02"
)

var _ adminconsole.AnalyticsReader = (*AdminConsole)(nil)

// windowStart is midnight UTC of the first day of a window that ends today.
func windowStart(now time.Time, days int) time.Time {
	today := now.UTC().Truncate(24 * time.Hour)
	return today.AddDate(0, 0, -(days - 1))
}

// fillDays turns the days that had rows into one entry per day of the window.
func fillDays(start time.Time, days int, found map[string]adminconsole.DayCount) []adminconsole.DayCount {
	out := make([]adminconsole.DayCount, 0, days)
	for offset := 0; offset < days; offset++ {
		key := start.AddDate(0, 0, offset).Format(dayLayout)
		entry := found[key]
		entry.Day = key
		out = append(out, entry)
	}
	return out
}

// customersSQL is the accounts the Users tab counts: people with the plain
// "user" role. Staff and service principals are left out, as are accounts
// older than the window.
const customersSQL = `
	WITH customers AS (
		SELECT u.id, u.email, u.created_at, u.email_verified_at, u.last_login_at, s.user_id IS NOT NULL AS suspended
		FROM users u LEFT JOIN abuse_suspensions s ON s.user_id = u.id
		WHERE u.kind = 'human' AND u.role = 'user' AND u.created_at >= $1
	)`

const (
	signupsPerDaySQL = customersSQL + `
		SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), COUNT(*), COUNT(*) FILTER (WHERE suspended)
		FROM customers GROUP BY 1`

	// The refused set is read once from the audit log by its timestamp index,
	// never per account: the log has no index on the user.
	signupTotalsSQL = customersSQL + `,
		refused AS (SELECT DISTINCT user_id FROM audit_log WHERE action IN ($3, $4) AND timestamp >= $1)
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE suspended),
		       COUNT(*) FILTER (WHERE NOT suspended AND last_login_at >= $2),
		       COUNT(*) FILTER (WHERE email_verified_at IS NOT NULL),
		       COUNT(*) FILTER (WHERE EXISTS (
		           SELECT 1 FROM orgs o JOIN database_instances di ON di.org_id = o.id WHERE o.owner_id = customers.id)),
		       COUNT(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM refused WHERE refused.user_id = customers.id))
		FROM customers`

	signupDomainsSQL = customersSQL + `
		SELECT lower(split_part(email, '@', 2)) AS domain, COUNT(*), COUNT(*) FILTER (WHERE suspended)
		FROM customers GROUP BY domain ORDER BY COUNT(*) DESC, domain LIMIT $2`
)

// UserAnalytics reads the Users tab over the last days days.
func (a *AdminConsole) UserAnalytics(ctx context.Context, days int) (*adminconsole.UserAnalytics, error) {
	now := time.Now()
	start := windowStart(now, days)
	result := &adminconsole.UserAnalytics{WindowDays: days, Funnel: []adminconsole.FunnelStep{}, Domains: []adminconsole.DomainStat{}}
	perDay, err := a.dayCounts(ctx, signupsPerDaySQL, start)
	if err != nil {
		return nil, fmt.Errorf("signups per day: %w", err)
	}
	result.PerDay = fillDays(start, days, perDay)
	var verified, withProject, neverRefused int
	if err := a.db.QueryRowContext(ctx, signupTotalsSQL, start, now.Add(-activeWindow), abuse.AuditActionProxyRefused, abuse.AuditActionRegistryRefused).
		Scan(&result.Signups, &result.Suspended, &result.ActiveLast7Days, &verified, &withProject, &neverRefused); err != nil {
		return nil, fmt.Errorf("signup totals: %w", err)
	}
	result.Funnel = []adminconsole.FunnelStep{
		{Label: "Signed up", Count: result.Signups}, {Label: "Verified email", Count: verified},
		{Label: "Created a project", Count: withProject}, {Label: "Never refused", Count: neverRefused},
	}
	if result.Domains, err = a.signupDomainStats(ctx, start); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *AdminConsole) signupDomainStats(ctx context.Context, start time.Time) ([]adminconsole.DomainStat, error) {
	rows, err := a.db.QueryContext(ctx, signupDomainsSQL, start, analyticsDomainLimit)
	if err != nil {
		return nil, fmt.Errorf("signup domains: %w", err)
	}
	defer rows.Close()
	out := []adminconsole.DomainStat{}
	for rows.Next() {
		var stat adminconsole.DomainStat
		if err := rows.Scan(&stat.Domain, &stat.Accounts, &stat.Suspended); err != nil {
			return nil, err
		}
		out = append(out, stat)
	}
	return out, rows.Err()
}

// dayCounts runs a query that answers (day, total, flagged) rows for a window starting at start.
func (a *AdminConsole) dayCounts(ctx context.Context, query string, start time.Time, args ...any) (map[string]adminconsole.DayCount, error) {
	rows, err := a.db.QueryContext(ctx, query, append([]any{start}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]adminconsole.DayCount{}
	for rows.Next() {
		var entry adminconsole.DayCount
		if err := rows.Scan(&entry.Day, &entry.Total, &entry.Flagged); err != nil {
			return nil, err
		}
		found[entry.Day] = entry
	}
	return found, rows.Err()
}

// refusalsSQL is the refused deploys of customers in the window, with their account.
const refusalsSQL = `
	WITH refusals AS (
		SELECT a.timestamp, a.user_id, a.project_id, a.action, left(a.details, 300) AS details,
		       COALESCE(u.email, '') AS email, u.active, s.user_id IS NOT NULL AS suspended
		FROM audit_log a
		JOIN users u ON u.id = a.user_id AND u.kind = 'human' AND u.role = 'user'
		LEFT JOIN abuse_suspensions s ON s.user_id = u.id
		WHERE a.action IN ($2, $3) AND a.timestamp >= $1
	)`

const (
	refusalsPerDaySQL = refusalsSQL + `
		SELECT to_char(timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD'), COUNT(*), COUNT(DISTINCT user_id)
		FROM refusals GROUP BY 1`

	refusalTotalsSQL = refusalsSQL + `
		SELECT COUNT(*) FILTER (WHERE action = $2),
		       COUNT(*) FILTER (WHERE action = $3),
		       COUNT(DISTINCT user_id),
		       COUNT(DISTINCT user_id) FILTER (WHERE active AND NOT suspended)
		FROM refusals`

	latestRefusalsSQL = refusalsSQL + `
		SELECT timestamp, user_id, project_id, action, details, email, suspended
		FROM refusals ORDER BY timestamp DESC LIMIT $4`
)

// AbuseAnalytics reads the Abuse tab over the last days days.
func (a *AdminConsole) AbuseAnalytics(ctx context.Context, days int) (*adminconsole.AbuseAnalytics, error) {
	start := windowStart(time.Now(), days)
	proxy, registry := abuse.AuditActionProxyRefused, abuse.AuditActionRegistryRefused
	result := &adminconsole.AbuseAnalytics{WindowDays: days, Latest: []adminconsole.RefusalBrief{}}
	perDay, err := a.dayCounts(ctx, refusalsPerDaySQL, start, proxy, registry)
	if err != nil {
		return nil, fmt.Errorf("refusals per day: %w", err)
	}
	result.PerDay = fillDays(start, days, perDay)
	if err := a.db.QueryRowContext(ctx, refusalTotalsSQL, start, proxy, registry).
		Scan(&result.ProxyRefusals, &result.RegistryRefusals, &result.RefusedUsers, &result.RefusedStillActive); err != nil {
		return nil, fmt.Errorf("refusal totals: %w", err)
	}
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM abuse_suspensions WHERE suspended_at >= $1`, start).Scan(&result.Suspended); err != nil {
		return nil, fmt.Errorf("suspensions in window: %w", err)
	}
	if result.Latest, err = a.latestRefusals(ctx, start, proxy, registry); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *AdminConsole) latestRefusals(ctx context.Context, start time.Time, proxy, registry string) ([]adminconsole.RefusalBrief, error) {
	rows, err := a.db.QueryContext(ctx, latestRefusalsSQL, start, proxy, registry, analyticsLatestLimit)
	if err != nil {
		return nil, fmt.Errorf("latest refusals: %w", err)
	}
	defer rows.Close()
	out := []adminconsole.RefusalBrief{}
	for rows.Next() {
		var brief adminconsole.RefusalBrief
		var at sql.NullTime
		var action string
		if err := rows.Scan(&at, &brief.UserID, &brief.ProjectID, &action, &brief.Details, &brief.Email, &brief.Suspended); err != nil {
			return nil, err
		}
		brief.At = at.Time
		brief.Kind = "proxy"
		if action == abuse.AuditActionRegistryRefused {
			brief.Kind = "registry"
		}
		out = append(out, brief)
	}
	return out, rows.Err()
}

const peakConnectionsSQL = `
	SELECT day, COALESCE(SUM(peak), 0)::int, 0
	FROM (
		SELECT project_id, to_char(timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day, MAX(active_connections) AS peak
		FROM database_metrics WHERE timestamp >= $1 GROUP BY project_id, day
	) per_project GROUP BY day`

// TrafficAnalytics reads the one traffic signal that is recorded: database connection peaks.
func (a *AdminConsole) TrafficAnalytics(ctx context.Context, days int) (*adminconsole.TrafficAnalytics, error) {
	start := windowStart(time.Now(), days)
	found, err := a.dayCounts(ctx, peakConnectionsSQL, start)
	if err != nil {
		return nil, fmt.Errorf("peak connections: %w", err)
	}
	result := &adminconsole.TrafficAnalytics{WindowDays: days, PeakConnectionsDay: fillDays(start, days, found)}
	for _, entry := range result.PeakConnectionsDay {
		result.PeakConnections = max(result.PeakConnections, entry.Total)
	}
	return result, nil
}

// CreditAnalytics counts FREE orgs by whether their credit is left, and what credit paused.
// It reads current state only, so the window is not used.
func (a *AdminConsole) CreditAnalytics(ctx context.Context, _ int) (*adminconsole.CreditAnalytics, error) {
	var result adminconsole.CreditAnalytics
	err := a.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE a.balance_micros > 0), count(*) FILTER (WHERE a.balance_micros <= 0)
		FROM credit_accounts a JOIN orgs o ON o.id = a.org_id WHERE upper(o.tier) = $1`, string(domain.Free)).
		Scan(&result.OrgsWithCredit, &result.OrgsAtZero)
	if err != nil {
		return nil, fmt.Errorf("credit accounts: %w", err)
	}
	err = a.db.QueryRowContext(ctx, `SELECT count(DISTINCT org_id), count(*) FROM credit_paused_services`).
		Scan(&result.OrgsWithPausedServices, &result.ServicesPaused)
	if err != nil {
		return nil, fmt.Errorf("services paused for credit: %w", err)
	}
	return &result, nil
}
