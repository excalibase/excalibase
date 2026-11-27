package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/credits"
)

const (
	creditStatementEntryCap = 10000
	creditExpiryBatch       = 500
	creditAdminKeyFormat    = "admin:%s:%s"
	creditExpireKeyFormat   = "expire:lot:%d"
	creditUncoveredKey      = "uncovered_micros"
)

// inCreditTx runs fn in one transaction; any error rolls everything back.
func (s *Store) inCreditTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// lockCreditAccount makes sure the org has an account row and locks it for the
// transaction, so debits, adjustments and expiries of one org run one at a time.
func lockCreditAccount(ctx context.Context, tx *sql.Tx, orgID string, now time.Time) (credits.Micros, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO credit_accounts (org_id, created_at, updated_at) VALUES ($1, $2, $2) ON CONFLICT (org_id) DO NOTHING`, orgID, now.UTC()); err != nil {
		return 0, err
	}
	var balance int64
	err := tx.QueryRowContext(ctx, `SELECT balance_micros FROM credit_accounts WHERE org_id = $1 FOR UPDATE`, orgID).Scan(&balance)
	return credits.Micros(balance), err
}

type newEntry struct {
	orgID       string
	kind        credits.EntryKind
	amount      credits.Micros
	key         string
	periodStart *time.Time
	periodEnd   *time.Time
	actor       string
	reason      string
	details     map[string]any
	at          time.Time
}

func insertCreditEntry(ctx context.Context, tx *sql.Tx, entry newEntry) (int64, error) {
	details := entry.details
	if details == nil {
		details = map[string]any{}
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO credit_entries (org_id, kind, amount_micros, idempotency_key, period_start, period_end, actor, reason, details, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		entry.orgID, entry.kind, int64(entry.amount), entry.key, entry.periodStart, entry.periodEnd, entry.actor, entry.reason, encoded, entry.at.UTC()).Scan(&id)
	return id, err
}

func addToCreditBalance(ctx context.Context, tx *sql.Tx, orgID string, delta credits.Micros, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE credit_accounts SET balance_micros = balance_micros + $2, version = version + 1, updated_at = $3 WHERE org_id = $1`,
		orgID, int64(delta), now.UTC())
	return err
}

func insertCreditLot(ctx context.Context, tx *sql.Tx, orgID string, entryID int64, amount credits.Micros, expiresAt *time.Time, now time.Time) error {
	var expires any
	if expiresAt != nil {
		expires = expiresAt.UTC()
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO credit_lots (org_id, source_entry_id, original_micros, remaining_micros, expires_at, created_at) VALUES ($1, $2, $3, $3, $4, $5)`,
		orgID, entryID, int64(amount), expires, now.UTC())
	return err
}

// openCreditLots reads and locks every lot of the org that still holds credit.
func openCreditLots(ctx context.Context, tx *sql.Tx, orgID string) ([]credits.Lot, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT l.id, e.kind, l.original_micros, l.remaining_micros, l.expires_at, l.created_at
		 FROM credit_lots l JOIN credit_entries e ON e.id = l.source_entry_id
		 WHERE l.org_id = $1 AND l.remaining_micros > 0 ORDER BY l.id FOR UPDATE OF l`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCreditLots(rows)
}

func scanCreditLots(rows *sql.Rows) ([]credits.Lot, error) {
	lots := []credits.Lot{}
	for rows.Next() {
		var lot credits.Lot
		var original, remaining int64
		var expires sql.NullTime
		if err := rows.Scan(&lot.ID, &lot.Kind, &original, &remaining, &expires, &lot.CreatedAt); err != nil {
			return nil, err
		}
		lot.Original, lot.Remaining = credits.Micros(original), credits.Micros(remaining)
		if expires.Valid {
			at := expires.Time.UTC()
			lot.ExpiresAt = &at
		}
		lots = append(lots, lot)
	}
	return lots, rows.Err()
}

// takeFromCreditLots records a negative entry's split across lots.
func takeFromCreditLots(ctx context.Context, tx *sql.Tx, entryID int64, draws []credits.Draw) error {
	for _, draw := range draws {
		if _, err := tx.ExecContext(ctx, `UPDATE credit_lots SET remaining_micros = remaining_micros - $2 WHERE id = $1`, draw.LotID, int64(draw.Amount)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO credit_lot_debits (entry_id, lot_id, amount_micros) VALUES ($1, $2, $3)`, entryID, draw.LotID, int64(draw.Amount)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreditAccount(ctx context.Context, orgID string) (credits.Account, error) {
	account := credits.Account{OrgID: orgID, Lots: []credits.Lot{}}
	var balance int64
	err := s.db.QueryRowContext(ctx, `SELECT balance_micros FROM credit_accounts WHERE org_id = $1`, orgID).Scan(&balance)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return account, err
	}
	account.Balance = credits.Micros(balance)
	rows, err := s.db.QueryContext(ctx,
		`SELECT l.id, e.kind, l.original_micros, l.remaining_micros, l.expires_at, l.created_at
		 FROM credit_lots l JOIN credit_entries e ON e.id = l.source_entry_id
		 WHERE l.org_id = $1 AND l.remaining_micros > 0 ORDER BY l.expires_at NULLS LAST, l.id`, orgID)
	if err != nil {
		return account, err
	}
	defer rows.Close()
	account.Lots, err = scanCreditLots(rows)
	return account, err
}

func (s *Store) CreditOrgExists(ctx context.Context, orgID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM orgs WHERE id = $1)`, orgID).Scan(&exists)
	return exists, err
}

func (s *Store) CreditMeterWatermark(ctx context.Context, orgID string) (time.Time, bool, error) {
	var mark time.Time
	err := s.db.QueryRowContext(ctx, `SELECT watermark FROM credit_meter_watermarks WHERE org_id = $1`, orgID).Scan(&mark)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return mark.UTC(), err == nil, err
}

// AdvanceCreditMeterWatermark moves the watermark forward; it never moves back.
func (s *Store) AdvanceCreditMeterWatermark(ctx context.Context, orgID string, to time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO credit_meter_watermarks (org_id, watermark) VALUES ($1, $2)
		 ON CONFLICT (org_id) DO UPDATE SET watermark = GREATEST(credit_meter_watermarks.watermark, EXCLUDED.watermark), updated_at = now()`,
		orgID, to.UTC())
	return err
}

func (s *Store) DebitCredit(ctx context.Context, request credits.DebitRequest) (credits.DebitResult, error) {
	if request.Amount <= 0 || request.IdempotencyKey == "" || request.OrgID == "" || !request.PeriodEnd.After(request.PeriodStart) {
		return credits.DebitResult{}, credits.ErrInvalidAmount
	}
	var result credits.DebitResult
	err := s.inCreditTx(ctx, func(tx *sql.Tx) error {
		var balance int64
		err := tx.QueryRowContext(ctx, `SELECT balance_micros FROM credit_accounts WHERE org_id = $1 FOR UPDATE`, request.OrgID).Scan(&balance)
		if errors.Is(err, sql.ErrNoRows) {
			result = credits.DebitResult{Uncovered: request.Amount}
			return nil
		}
		if err != nil {
			return err
		}
		if replay, found, err := findCreditDebit(ctx, tx, request); err != nil || found {
			result = replay
			return err
		}
		lots, err := openCreditLots(ctx, tx, request.OrgID)
		if err != nil {
			return err
		}
		draws, taken := credits.PlanDraw(lots, request.Amount, request.PeriodEnd)
		details := map[string]any{}
		for key, value := range request.Details {
			details[key] = value
		}
		details[creditUncoveredKey] = int64(request.Amount - taken)
		start, end := request.PeriodStart.UTC(), request.PeriodEnd.UTC()
		entryID, err := insertCreditEntry(ctx, tx, newEntry{orgID: request.OrgID, kind: credits.KindUsage, amount: -taken, key: request.IdempotencyKey,
			periodStart: &start, periodEnd: &end, actor: "system", details: details, at: time.Now()})
		if err != nil {
			return err
		}
		if err := takeFromCreditLots(ctx, tx, entryID, draws); err != nil {
			return err
		}
		if err := addToCreditBalance(ctx, tx, request.OrgID, -taken, time.Now()); err != nil {
			return err
		}
		result = credits.DebitResult{EntryID: entryID, Taken: taken, Uncovered: request.Amount - taken}
		return nil
	})
	return result, err
}

// findCreditDebit answers a repeated debit with what the first one did.
func findCreditDebit(ctx context.Context, tx *sql.Tx, request credits.DebitRequest) (credits.DebitResult, bool, error) {
	var entryID, amount, uncovered int64
	var orgID string
	err := tx.QueryRowContext(ctx,
		`SELECT id, org_id, amount_micros, COALESCE((details->>'uncovered_micros')::bigint, 0) FROM credit_entries WHERE idempotency_key = $1`,
		request.IdempotencyKey).Scan(&entryID, &orgID, &amount, &uncovered)
	if errors.Is(err, sql.ErrNoRows) {
		return credits.DebitResult{}, false, nil
	}
	if err != nil {
		return credits.DebitResult{}, false, err
	}
	if orgID != request.OrgID || credits.Micros(-amount+uncovered) != request.Amount {
		return credits.DebitResult{}, false, credits.ErrIdempotencyConflict
	}
	return credits.DebitResult{EntryID: entryID, Taken: credits.Micros(-amount), Uncovered: credits.Micros(uncovered), Replayed: true}, true, nil
}

func (s *Store) AdjustCredit(ctx context.Context, adjustment credits.Adjustment, now time.Time) (credits.Entry, error) {
	if adjustment.Amount == 0 || adjustment.IdempotencyKey == "" || adjustment.OrgID == "" {
		return credits.Entry{}, credits.ErrInvalidAmount
	}
	key := fmt.Sprintf(creditAdminKeyFormat, adjustment.OrgID, adjustment.IdempotencyKey)
	var entry credits.Entry
	err := s.inCreditTx(ctx, func(tx *sql.Tx) error {
		balance, err := lockCreditAccount(ctx, tx, adjustment.OrgID, now)
		if err != nil {
			return err
		}
		if existing, found, err := findCreditEntryByKey(ctx, tx, key); err != nil || found {
			if err == nil {
				err = checkSameAdjustment(ctx, tx, existing, adjustment)
			}
			entry = existing
			return err
		}
		if adjustment.Amount < 0 && -adjustment.Amount > balance {
			return credits.ErrInsufficientBalance
		}
		entry, err = applyAdjustment(ctx, tx, adjustment, key, now)
		if err != nil {
			return err
		}
		return insertCreditAudit(ctx, tx, adjustment.Audit, now)
	})
	return entry, err
}

func applyAdjustment(ctx context.Context, tx *sql.Tx, adjustment credits.Adjustment, key string, now time.Time) (credits.Entry, error) {
	kind := credits.KindGrantAdmin
	if adjustment.Amount < 0 {
		kind = credits.KindAdjustAdmin
	}
	id, err := insertCreditEntry(ctx, tx, newEntry{orgID: adjustment.OrgID, kind: kind, amount: adjustment.Amount, key: key,
		actor: adjustment.Actor, reason: adjustment.Reason, at: now})
	if err != nil {
		return credits.Entry{}, err
	}
	if adjustment.Amount > 0 {
		err = insertCreditLot(ctx, tx, adjustment.OrgID, id, adjustment.Amount, adjustment.ExpiresAt, now)
	} else {
		err = takeFromAllLots(ctx, tx, adjustment.OrgID, id, -adjustment.Amount)
	}
	if err != nil {
		return credits.Entry{}, err
	}
	if err := addToCreditBalance(ctx, tx, adjustment.OrgID, adjustment.Amount, now); err != nil {
		return credits.Entry{}, err
	}
	return credits.Entry{ID: id, OrgID: adjustment.OrgID, Kind: kind, Amount: adjustment.Amount, Actor: adjustment.Actor, Reason: adjustment.Reason, CreatedAt: now.UTC(), Details: map[string]any{}}, nil
}

// takeFromAllLots draws an adjustment from every lot, expired ones the sweeper
// has not reached yet included, because the balance includes them.
func takeFromAllLots(ctx context.Context, tx *sql.Tx, orgID string, entryID int64, amount credits.Micros) error {
	lots, err := openCreditLots(ctx, tx, orgID)
	if err != nil {
		return err
	}
	draws, taken := credits.PlanDraw(lots, amount, time.Time{})
	if taken < amount {
		return credits.ErrInsufficientBalance
	}
	return takeFromCreditLots(ctx, tx, entryID, draws)
}

func insertCreditAudit(ctx context.Context, tx *sql.Tx, audit credits.AuditRecord, now time.Time) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (user_id, action, resource, resource_id, details, ip_address, timestamp) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		audit.UserID, audit.Action, audit.Resource, audit.ResourceID, audit.Details, audit.IPAddress, now.UTC())
	return err
}

func findCreditEntryByKey(ctx context.Context, tx *sql.Tx, key string) (credits.Entry, bool, error) {
	row := tx.QueryRowContext(ctx, creditEntryColumns+` FROM credit_entries WHERE idempotency_key = $1`, key)
	entry, err := scanCreditEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return credits.Entry{}, false, nil
	}
	return entry, err == nil, err
}

func (s *Store) ExpireCreditLots(ctx context.Context, now time.Time, requireWatermark bool) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT l.id, l.org_id FROM credit_lots l LEFT JOIN credit_meter_watermarks w ON w.org_id = l.org_id
		 WHERE l.remaining_micros > 0 AND l.expires_at IS NOT NULL AND l.expires_at <= $1
		   AND l.expires_at <= CASE WHEN $3::boolean THEN w.watermark ELSE COALESCE(w.watermark, $1) END
		 ORDER BY l.expires_at, l.id LIMIT $2`, now.UTC(), creditExpiryBatch, requireWatermark)
	if err != nil {
		return 0, err
	}
	type due struct {
		lotID int64
		orgID string
	}
	var lots []due
	for rows.Next() {
		var lot due
		if err := rows.Scan(&lot.lotID, &lot.orgID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		lots = append(lots, lot)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	expired := 0
	for _, lot := range lots {
		done, err := s.expireCreditLot(ctx, lot.lotID, lot.orgID, now, requireWatermark)
		if err != nil {
			return expired, err
		}
		if done {
			expired++
		}
	}
	return expired, nil
}

func (s *Store) expireCreditLot(ctx context.Context, lotID int64, orgID string, now time.Time, requireWatermark bool) (bool, error) {
	done := false
	err := s.inCreditTx(ctx, func(tx *sql.Tx) error {
		if _, err := lockCreditAccount(ctx, tx, orgID, now); err != nil {
			return err
		}
		var remaining int64
		err := tx.QueryRowContext(ctx,
			`SELECT l.remaining_micros FROM credit_lots l LEFT JOIN credit_meter_watermarks w ON w.org_id = l.org_id
			 WHERE l.id = $1 AND l.remaining_micros > 0 AND l.expires_at <= $2
			   AND l.expires_at <= CASE WHEN $3::boolean THEN w.watermark ELSE COALESCE(w.watermark, $2) END FOR UPDATE OF l`,
			lotID, now.UTC(), requireWatermark).Scan(&remaining)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		amount := credits.Micros(remaining)
		entryID, err := insertCreditEntry(ctx, tx, newEntry{orgID: orgID, kind: credits.KindExpire, amount: -amount, key: fmt.Sprintf(creditExpireKeyFormat, lotID),
			actor: "system", reason: "credit expired", details: map[string]any{"lotId": lotID}, at: now})
		if err != nil {
			return err
		}
		if err := takeFromCreditLots(ctx, tx, entryID, []credits.Draw{{LotID: lotID, Amount: amount}}); err != nil {
			return err
		}
		done = true
		return addToCreditBalance(ctx, tx, orgID, -amount, now)
	})
	return done, err
}

var _ credits.Ledger = (*Store)(nil)

// checkSameAdjustment refuses a replayed key that asks for something else than the first request did.
func checkSameAdjustment(ctx context.Context, tx *sql.Tx, existing credits.Entry, adjustment credits.Adjustment) error {
	if existing.OrgID != adjustment.OrgID || existing.Amount != adjustment.Amount || existing.Reason != adjustment.Reason {
		return credits.ErrIdempotencyConflict
	}
	if adjustment.Amount < 0 {
		return nil
	}
	var stored sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM credit_lots WHERE source_entry_id = $1`, existing.ID).Scan(&stored); err != nil {
		return err
	}
	if stored.Valid != (adjustment.ExpiresAt != nil) || (stored.Valid && !stored.Time.Equal(adjustment.ExpiresAt.Truncate(time.Microsecond))) {
		return credits.ErrIdempotencyConflict
	}
	return nil
}
