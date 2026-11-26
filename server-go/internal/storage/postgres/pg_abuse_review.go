package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/behaviour"
	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

// ErrReviewNotOpen is a dismissal of a review that is unknown or already closed.
var ErrReviewNotOpen = errors.New("no open review with that id")

// RecordFinding stores a finding by these rules: one open review per owner,
// app and signal, whose repeats only move its last-seen time, peak and
// evidence; a finding inside a dismissal's mute is dropped unless it has grown
// GrowthFactor times past the dismissed peak; anything else opens a review.
func (s *Store) RecordFinding(ctx context.Context, f behaviour.Finding, now time.Time, rules behaviour.Rules) (behaviour.Outcome, error) {
	if !behaviour.ValidEvidence(f.Evidence) {
		return "", errors.New("review evidence may carry only the fixed numeric keys")
	}
	evidence, err := json.Marshal(f.Evidence)
	if err != nil {
		return "", fmt.Errorf("review evidence: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin review: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	outcome, err := recordFindingIn(ctx, tx, f, evidence, now.UTC(), rules)
	if err != nil {
		return "", err
	}
	return outcome, tx.Commit()
}

const touchOpenReviewSQL = `UPDATE abuse_review SET last_seen = $4, peak = GREATEST(peak, $5), evidence = $6,
	org_id = $7, project_id = $8, project_name = $9, app_name = $10, tier = $11
	WHERE owner_id = $1 AND app_id = $2 AND signal = $3 AND status = 'open'`

const insertReviewSQL = `INSERT INTO abuse_review (owner_id, org_id, project_id, project_name, app_id, app_name, tier, signal, first_seen, last_seen, peak, evidence)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$10,$11)
	ON CONFLICT (owner_id, app_id, signal) WHERE status = 'open' DO UPDATE SET last_seen = EXCLUDED.last_seen,
		peak = GREATEST(abuse_review.peak, EXCLUDED.peak), evidence = EXCLUDED.evidence
	RETURNING (xmax = 0)`

func recordFindingIn(ctx context.Context, tx *sql.Tx, f behaviour.Finding, evidence []byte, now time.Time, rules behaviour.Rules) (behaviour.Outcome, error) {
	touched, err := tx.ExecContext(ctx, touchOpenReviewSQL, f.OwnerID, f.AppID, f.Signal, now, f.Magnitude, evidence,
		f.OrgID, f.ProjectID, f.ProjectName, f.AppName, f.Tier)
	if err != nil {
		return "", fmt.Errorf("update review: %w", err)
	}
	if rows, _ := touched.RowsAffected(); rows > 0 {
		return behaviour.OutcomeUpdated, nil
	}
	var dismissedPeak float64
	var muteUntil sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT dismissed_peak, mute_until FROM abuse_review
		WHERE owner_id = $1 AND app_id = $2 AND signal = $3 AND status = 'dismissed'
		ORDER BY dismissed_at DESC LIMIT 1`, f.OwnerID, f.AppID, f.Signal).Scan(&dismissedPeak, &muteUntil)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read dismissal: %w", err)
	}
	if err == nil && muteUntil.Valid && now.Before(muteUntil.Time) && f.Magnitude < dismissedPeak*rules.GrowthFactor {
		return behaviour.OutcomeSuppressed, nil
	}
	var inserted bool
	if err := tx.QueryRowContext(ctx, insertReviewSQL, f.OwnerID, f.OrgID, f.ProjectID, f.ProjectName, f.AppID, f.AppName, f.Tier,
		f.Signal, now, f.Magnitude, evidence).Scan(&inserted); err != nil {
		return "", fmt.Errorf("insert review: %w", err)
	}
	if !inserted {
		return behaviour.OutcomeUpdated, nil
	}
	return behaviour.OutcomeOpened, nil
}

// DismissReview closes an open review and mutes its key for muteDays, or until
// the finding grows past the peak it had when dismissed.
func (s *Store) DismissReview(ctx context.Context, id int64, actor string, now time.Time, muteDays int) error {
	now = now.UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE abuse_review SET status = 'dismissed', dismissed_at = $2, dismissed_by = $3,
		dismissed_peak = peak, mute_until = $4 WHERE id = $1 AND status = 'open'`, id, now, actor, now.Add(time.Duration(muteDays)*24*time.Hour))
	if err != nil {
		return fmt.Errorf("dismiss review: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ErrReviewNotOpen
	}
	return nil
}

// ListReviews reads reviews, newest activity first; an empty status reads every one.
func (s *Store) ListReviews(ctx context.Context, status string) ([]behaviour.Review, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, org_id, project_id, project_name, app_id, app_name, tier, signal, status,
		first_seen, last_seen, peak, evidence, dismissed_at, dismissed_by, dismissed_peak, mute_until
		FROM abuse_review WHERE $1 = '' OR status = $1 ORDER BY last_seen DESC, id DESC`, status)
	if err != nil {
		return nil, fmt.Errorf("list reviews: %w", err)
	}
	defer rows.Close()
	var out []behaviour.Review
	for rows.Next() {
		var r behaviour.Review
		var evidence []byte
		var dismissedAt, muteUntil sql.NullTime
		if err := rows.Scan(&r.ID, &r.OwnerID, &r.OrgID, &r.ProjectID, &r.ProjectName, &r.AppID, &r.AppName, &r.Tier, &r.Signal, &r.Status,
			&r.FirstSeen, &r.LastSeen, &r.Peak, &evidence, &dismissedAt, &r.DismissedBy, &r.DismissedPeak, &muteUntil); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(evidence, &r.Evidence); err != nil {
			return nil, fmt.Errorf("review %d evidence: %w", r.ID, err)
		}
		r.FirstSeen, r.LastSeen = r.FirstSeen.UTC(), r.LastSeen.UTC()
		if dismissedAt.Valid {
			at := dismissedAt.Time.UTC()
			r.DismissedAt = &at
		}
		if muteUntil.Valid {
			until := muteUntil.Time.UTC()
			r.MuteUntil = &until
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneReviews drops dismissed reviews older than the retention. Open and
// actioned ones stay: an actioned review is the record of why an account was acted on.
func (s *Store) PruneReviews(ctx context.Context, now time.Time, dismissedRetentionDays int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM abuse_review WHERE status = 'dismissed' AND dismissed_at < $1`,
		now.UTC().Add(-time.Duration(dismissedRetentionDays)*24*time.Hour))
	if err != nil {
		return fmt.Errorf("prune reviews: %w", err)
	}
	return nil
}

// RecentNetMinutes reads every app's minute rows since a time, oldest first.
func (s *Store) RecentNetMinutes(ctx context.Context, since time.Time) ([]netmeter.Sample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, project_id, app_id, instances, rx_bps, tx_bps, max_instance_tx_bps
		FROM app_net_samples WHERE ts >= $1 ORDER BY ts`, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("recent net minutes: %w", err)
	}
	defer rows.Close()
	var out []netmeter.Sample
	for rows.Next() {
		var sample netmeter.Sample
		if err := rows.Scan(&sample.At, &sample.ProjectID, &sample.AppID, &sample.Instances, &sample.RxBps, &sample.TxBps, &sample.MaxInstanceTxBps); err != nil {
			return nil, err
		}
		sample.At = sample.At.UTC()
		out = append(out, sample)
	}
	return out, rows.Err()
}

// volumeSQL adds up bytes per app from the minute rows (24 hours) and from
// the hourly rows plus the minutes of hours not rolled up yet (30 days). The
// two parts of the month never overlap: an hour is rolled up whole, and only
// minutes after the last rolled hour are counted from the minute table.
const volumeSQL = `
	SELECT project_id, app_id, 'day', SUM(tx_bps * 60), SUM(rx_bps * 60)
	FROM app_net_samples WHERE ts >= $1 GROUP BY project_id, app_id
	UNION ALL
	SELECT project_id, app_id, 'month', SUM(avg_tx_bps * samples * 60), SUM(avg_rx_bps * samples * 60)
	FROM app_net_hourly WHERE hour >= $2 GROUP BY project_id, app_id
	UNION ALL
	SELECT project_id, app_id, 'month', SUM(tx_bps * 60), SUM(rx_bps * 60)
	FROM app_net_samples
	WHERE ts >= $2 AND ts >= COALESCE((SELECT MAX(hour) + INTERVAL '1 hour' FROM app_net_hourly), '-infinity'::timestamptz)
	GROUP BY project_id, app_id`

// NetVolumes is each app's bytes sent and received over the last 24 hours and 30 days.
func (s *Store) NetVolumes(ctx context.Context, now time.Time) ([]behaviour.AppVolume, error) {
	rows, err := s.db.QueryContext(ctx, volumeSQL, now.UTC().Add(-24*time.Hour), now.UTC().Add(-30*24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("net volumes: %w", err)
	}
	defer rows.Close()
	type key struct{ project, app string }
	byApp := map[key]*behaviour.AppVolume{}
	var order []key
	for rows.Next() {
		var project, app, period string
		var tx, rx float64
		if err := rows.Scan(&project, &app, &period, &tx, &rx); err != nil {
			return nil, err
		}
		id := key{project, app}
		volume := byApp[id]
		if volume == nil {
			volume = &behaviour.AppVolume{ProjectID: project, AppID: app}
			byApp[id] = volume
			order = append(order, id)
		}
		if period == "day" {
			volume.TxDay, volume.RxDay = tx, rx
		} else {
			volume.Tx30d += tx
			volume.Rx30d += rx
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]behaviour.AppVolume, 0, len(order))
	for _, id := range order {
		out = append(out, *byApp[id])
	}
	return out, nil
}

// ReviewDirectory names every live app and every project with its owner, org
// and tier, as the detector needs to attribute traffic. A project the owner
// has deleted is not here; its past findings keep their own snapshot.
func (s *Store) ReviewDirectory(ctx context.Context) ([]behaviour.Subject, map[string]behaviour.Subject, error) {
	projects := map[string]behaviour.Subject{}
	rows, err := s.db.QueryContext(ctx, `SELECT di.project_id, COALESCE(di.project_name, ''), di.org_id, o.owner_id, o.tier
		FROM database_instances di JOIN orgs o ON o.id = di.org_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("directory projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var subject behaviour.Subject
		if err := rows.Scan(&subject.ProjectID, &subject.ProjectName, &subject.OrgID, &subject.OwnerID, &subject.Tier); err != nil {
			return nil, nil, err
		}
		subject.Tier = strings.ToLower(subject.Tier)
		projects[subject.ProjectID] = subject
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	appRows, err := s.db.QueryContext(ctx, `SELECT project_id, id, name FROM apps ORDER BY project_id, id`)
	if err != nil {
		return nil, nil, fmt.Errorf("directory apps: %w", err)
	}
	defer appRows.Close()
	var apps []behaviour.Subject
	for appRows.Next() {
		var projectID, appID, name string
		if err := appRows.Scan(&projectID, &appID, &name); err != nil {
			return nil, nil, err
		}
		subject, ok := projects[projectID]
		if !ok {
			continue
		}
		subject.AppID, subject.AppName = appID, name
		apps = append(apps, subject)
	}
	return apps, projects, appRows.Err()
}
