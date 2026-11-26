package behaviour

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

// History reads what the meter stored.
type History interface {
	RecentNetMinutes(ctx context.Context, since time.Time) ([]netmeter.Sample, error)
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

type appKey struct{ project, app string }

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
	volumes, err := d.deps.History.NetVolumes(ctx, now)
	if err != nil {
		log.Printf("behaviour review: volumes: %v", err)
		return
	}
	series := map[appKey][]netmeter.Sample{}
	for _, minute := range minutes {
		key := appKey{minute.ProjectID, minute.AppID}
		series[key] = append(series[key], minute)
	}
	totals := map[appKey]Totals{}
	for _, volume := range volumes {
		totals[appKey{volume.ProjectID, volume.AppID}] = volume.Totals
	}
	var findings []Finding
	for _, app := range apps {
		key := appKey{app.ProjectID, app.AppID}
		findings = append(findings, Evaluate(d.cfg, app, d.cfg.Reference(app.Tier), series[key], totals[key])...)
	}
	findings = append(findings, d.ownerFindings(apps, projects, series, totals)...)
	rules := Rules{MuteDays: d.cfg.MuteDays, GrowthFactor: d.cfg.GrowthFactor}
	for _, finding := range findings {
		if _, err := d.deps.Reviews.RecordFinding(ctx, finding, now, rules); err != nil {
			log.Printf("behaviour review: record %s for owner %s: %v", finding.Signal, finding.OwnerID, err)
		}
	}
}

// ownerFindings judges each owner's traffic as a whole: every app it runs,
// and every deleted app of a project it still has, so splitting a workload
// across apps, or deleting and recreating one, does not reset the evidence.
func (d *Detector) ownerFindings(apps []Subject, projects map[string]Subject, series map[appKey][]netmeter.Sample, totals map[appKey]Totals) []Finding {
	type owner struct {
		subject Subject
		minutes [][]netmeter.Sample
		totals  Totals
		tiers   []string
		apps    map[appKey]bool
	}
	owners := map[string]*owner{}
	touch := func(project Subject) *owner {
		entry := owners[project.OwnerID]
		if entry == nil {
			entry = &owner{subject: Subject{OwnerID: project.OwnerID}, apps: map[appKey]bool{}}
			owners[project.OwnerID] = entry
		}
		return entry
	}
	for _, app := range apps {
		entry := touch(app)
		entry.tiers = append(entry.tiers, app.Tier)
	}
	seen := map[appKey]bool{}
	attribute := func(key appKey) {
		project, ok := projects[key.project]
		if !ok || seen[key] {
			return
		}
		seen[key] = true
		entry := touch(project)
		entry.apps[key] = true
		entry.minutes = append(entry.minutes, series[key])
		entry.totals.TxDay += totals[key].TxDay
		entry.totals.RxDay += totals[key].RxDay
		entry.totals.Tx30d += totals[key].Tx30d
		entry.totals.Rx30d += totals[key].Rx30d
	}
	for key := range series {
		attribute(key)
	}
	for key := range totals {
		attribute(key)
	}
	ids := make([]string, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var findings []Finding
	for _, id := range ids {
		entry := owners[id]
		reference := OwnerReference(d.cfg.ReferenceMbps, d.cfg.DefaultReferenceMbps, entry.tiers)
		for _, finding := range Evaluate(d.cfg, entry.subject, reference, SumByTimestamp(entry.minutes), entry.totals) {
			finding.Evidence["apps"] = float64(len(entry.apps))
			findings = append(findings, finding)
		}
	}
	return findings
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
