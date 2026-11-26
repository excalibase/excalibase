package adminconsole

import (
	"context"
	"time"
)

// AnalyticsWindowDays is how far back every analytics read looks. Each query is
// bounded by it, so a growing audit log or user table never makes a tab slow.
const AnalyticsWindowDays = 30

// TrafficWindowDays is the traffic tab's window. Sampled metrics are kept for
// seven days, so a longer window would show empty bars that read as "no traffic".
const TrafficWindowDays = 7

// DayCount is one calendar day (UTC) of a series; days with nothing are present with zero.
type DayCount struct {
	Day   string `json:"day"`
	Total int    `json:"total"`
	// Flagged is the part of Total that went on to be suspended (sign-ups) or
	// the number of distinct users behind it (refusals).
	Flagged int `json:"flagged,omitempty"`
}

// FunnelStep is a named count of the window's sign-ups.
type FunnelStep struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// DomainStat is a sign-up email domain, how many accounts used it and how many of them were suspended.
type DomainStat struct {
	Domain    string `json:"domain"`
	Accounts  int    `json:"accounts"`
	Suspended int    `json:"suspended"`
}

// UserAnalytics is the Users tab: customers only, staff and service accounts left out.
type UserAnalytics struct {
	WindowDays int `json:"windowDays"`
	Signups    int `json:"signups"`
	Suspended  int `json:"suspended"`
	// ActiveLast7Days are accounts of the window that signed in during the last week and are not suspended.
	ActiveLast7Days int          `json:"activeLast7Days"`
	PerDay          []DayCount   `json:"perDay"`
	Funnel          []FunnelStep `json:"funnel"`
	Domains         []DomainStat `json:"domains"`
}

// RefusalBrief is one refused deploy.
type RefusalBrief struct {
	At        time.Time `json:"at"`
	ProjectID string    `json:"projectId"`
	UserID    string    `json:"userId"`
	Email     string    `json:"email"`
	// Kind is "proxy" or "registry".
	Kind    string `json:"kind"`
	Details string `json:"details"`
	// Suspended says whether the account has been suspended since.
	Suspended bool `json:"suspended"`
}

// AbuseAnalytics is the Abuse tab: what the app guard refused.
type AbuseAnalytics struct {
	WindowDays int `json:"windowDays"`
	// PerDay counts refusals (Total) and distinct users (Flagged).
	PerDay           []DayCount `json:"perDay"`
	ProxyRefusals    int        `json:"proxyRefusals"`
	RegistryRefusals int        `json:"registryRefusals"`
	RefusedUsers     int        `json:"refusedUsers"`
	// RefusedStillActive are refused customers whose account is neither suspended nor disabled:
	// a refusal stops the app, not the account.
	RefusedStillActive int            `json:"refusedStillActive"`
	Suspended          int            `json:"suspended"`
	Latest             []RefusalBrief `json:"latest"`
}

// TrafficAnalytics is the Traffic tab. Only database connection peaks are
// recorded today; request and network collectors are not part of it.
type TrafficAnalytics struct {
	WindowDays         int        `json:"windowDays"`
	PeakConnectionsDay []DayCount `json:"peakConnectionsPerDay"`
	PeakConnections    int        `json:"peakConnections"`
}

// AnalyticsReader is every query the analytics tabs are built from.
type AnalyticsReader interface {
	UserAnalytics(ctx context.Context, days int) (*UserAnalytics, error)
	AbuseAnalytics(ctx context.Context, days int) (*AbuseAnalytics, error)
	TrafficAnalytics(ctx context.Context, days int) (*TrafficAnalytics, error)
}
