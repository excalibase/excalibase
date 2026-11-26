package behaviour

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

// History reads what the meter stored.
type History interface {
	RecentNetMinutes(ctx context.Context, since time.Time) ([]netmeter.Sample, error)
	// RecentNetHours is the minutes since a time folded into hours, one row per app and hour.
	RecentNetHours(ctx context.Context, since time.Time) ([]netmeter.Hourly, error)
	NetVolumes(ctx context.Context, now time.Time) ([]AppVolume, error)
}

// Directory says who owns what.
type Directory interface {
	// ReviewDirectory returns every live app and every project, each with its owner, org and tier.
	ReviewDirectory(ctx context.Context) (apps []Subject, projects map[string]Subject, err error)
}

// Reviews keeps the findings for a person to look at.
type Reviews interface {
	RecordFinding(ctx context.Context, f Finding, now time.Time, rules Rules) (Outcome, error)
	PruneReviews(ctx context.Context, now time.Time, dismissedRetentionDays int) error
}

// DetectorDeps wires a Detector. It can read, and it can record a finding;
// it holds nothing that stops, limits or suspends anything.
type DetectorDeps struct {
	History   History
	Directory Directory
	Reviews   Reviews
}

// Detector looks at the meter's history on the metrics loop and records what
// behaves like a proxy. It does not act on any app or account.
type Detector struct {
	deps DetectorDeps
	cfg  Config
	now  func() time.Time

	lastEval  time.Time
	lastPrune time.Time
}

const pruneEvery = 24 * time.Hour

func NewDetector(deps DetectorDeps, cfg Config) *Detector {
	return &Detector{deps: deps, cfg: cfg, now: time.Now}
}

// Sample is one tick of the metrics loop; the detector looks at most every cfg.EvalEvery.
func (d *Detector) Sample(ctx context.Context) {
	now := d.now()
	if !d.lastEval.IsZero() && now.Sub(d.lastEval) < d.cfg.EvalEvery {
		return
	}
	d.lastEval = now
	d.evaluate(ctx, now)
	d.pruneIfDue(ctx, now)
}

const dayWindow = 24 * time.Hour

func (d *Detector) evaluate(ctx context.Context, now time.Time) {
	apps, projects, err := d.deps.Directory.ReviewDirectory(ctx)
	if err != nil {
		log.Printf("behaviour review: directory: %v", err)
		return
	}
	minutes, err := d.deps.History.RecentNetMinutes(ctx, now.Add(-time.Duration(d.cfg.WindowMinutes)*time.Minute))
	if err != nil {
		log.Printf("behaviour review: recent minutes: %v", err)
		return
	}
	hours, err := d.deps.History.RecentNetHours(ctx, now.Add(-dayWindow))
	if err != nil {
		log.Printf("behaviour review: recent hours: %v", err)
		return
	}
	volumes, err := d.deps.History.NetVolumes(ctx, now)
	if err != nil {
		log.Printf("behaviour review: volumes: %v", err)
		return
	}
	seen := newObservations(minutes, hours, volumes)
	var findings []Finding
	for _, app := range apps {
		key := appKey{app.ProjectID, app.AppID}
		findings = append(findings, Evaluate(d.cfg, app, d.cfg.Reference(app.Tier), seen.app(key))...)
	}
	findings = append(findings, d.ownerFindings(projects, seen)...)
	rules := Rules{MuteDays: d.cfg.MuteDays, GrowthFactor: d.cfg.GrowthFactor}
	for _, finding := range findings {
		if _, err := d.deps.Reviews.RecordFinding(ctx, finding, now, rules); err != nil {
			log.Printf("behaviour review: record %s for owner %s: %v", finding.Signal, finding.OwnerID, err)
		}
	}
}

// ownerFindings judges each owner's traffic per tier: every app it ran on
// that tier, live or deleted, using the owner and tier stored with the
// traffic, so splitting a workload across apps, or deleting and recreating
// one, does not reset the evidence.
func (d *Detector) ownerFindings(projects map[string]Subject, seen observations) []Finding {
	owners := newAttributor(projects)
	for _, key := range seen.keys() {
		owners.add(key, seen)
	}
	return owners.findings(d.cfg)
}

func (d *Detector) pruneIfDue(ctx context.Context, now time.Time) {
	if !d.lastPrune.IsZero() && now.Sub(d.lastPrune) < pruneEvery {
		return
	}
	d.lastPrune = now
	if err := d.deps.Reviews.PruneReviews(ctx, now, d.cfg.DismissedRetentionDays); err != nil {
		log.Printf("behaviour review: prune: %v", err)
	}
}
