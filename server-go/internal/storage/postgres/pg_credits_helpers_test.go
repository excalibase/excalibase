//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const creditsTerms = "terms-1"

var creditsNow = time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)

func trialRules(trialCap int) credits.Rules {
	return credits.Rules{TrialAmount: credits.FromDollars(300), TrialDays: 90, TrialCap: trialCap, AdminCap: credits.FromDollars(1000), TermsVersion: creditsTerms}
}

// creditPerson seeds a verified account that owns one free org and accepted the terms.
func creditPerson(t *testing.T, store *Store, id string) string {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateUser(ctx, userNamed(id, id)); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
	if err := store.MarkEmailVerified(ctx, id, creditsNow); err != nil {
		t.Fatalf("verify %s: %v", id, err)
	}
	orgID := "org-" + id
	if err := store.CreateOrgWithOwner(ctx, ownedOrg(orgID, orgID, id, domain.Free), 1); err != nil {
		t.Fatalf("seed org %s: %v", id, err)
	}
	if err := store.AcceptCreditTerms(ctx, id, creditsTerms, creditsNow); err != nil {
		t.Fatalf("accept terms %s: %v", id, err)
	}
	return orgID
}

// fundOrg gives an org one admin lot so debit tests need no person.
func fundOrg(t *testing.T, store *Store, orgID, key string, amount credits.Micros, expiresAt *time.Time) credits.Entry {
	t.Helper()
	entry, err := store.AdjustCredit(context.Background(), credits.Adjustment{
		OrgID: orgID, Amount: amount, Actor: "admin-1", Reason: "seed credit for a test",
		IdempotencyKey: key, ExpiresAt: expiresAt,
		Audit: credits.AuditRecord{UserID: "admin-1", Action: "admin.credit_adjust", Resource: "org", Details: "seed"},
	}, creditsNow)
	if err != nil {
		t.Fatalf("fund %s: %v", orgID, err)
	}
	return entry
}

func inHours(hours int) *time.Time {
	at := creditsNow.Add(time.Duration(hours) * time.Hour)
	return &at
}

// creditInvariant checks sum(lots) = balance = sum(entries) for one org.
func creditInvariant(t *testing.T, store *Store, orgID string) credits.Micros {
	t.Helper()
	var balance, lots, entries int64
	row := store.db.QueryRow(`SELECT
		COALESCE((SELECT balance_micros FROM credit_accounts WHERE org_id = $1), 0),
		COALESCE((SELECT SUM(remaining_micros) FROM credit_lots WHERE org_id = $1), 0),
		COALESCE((SELECT SUM(amount_micros) FROM credit_entries WHERE org_id = $1), 0)`, orgID)
	if err := row.Scan(&balance, &lots, &entries); err != nil {
		t.Fatal(err)
	}
	if balance != lots || balance != entries {
		t.Fatalf("org %s: balance %d, lots %d, entries %d must all agree", orgID, balance, lots, entries)
	}
	return credits.Micros(balance)
}
