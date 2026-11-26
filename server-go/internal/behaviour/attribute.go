package behaviour

import (
	"sort"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

type appKey struct{ project, app string }

// observations is everything the meter kept, by app.
type observations struct {
	minutes map[appKey][]netmeter.Sample
	hours   map[appKey][]netmeter.Hourly
	volumes map[appKey]AppVolume
}

func newObservations(minutes []netmeter.Sample, hours []netmeter.Hourly, volumes []AppVolume) observations {
	seen := observations{
		minutes: map[appKey][]netmeter.Sample{},
		hours:   map[appKey][]netmeter.Hourly{},
		volumes: map[appKey]AppVolume{},
	}
	for _, minute := range minutes {
		key := appKey{minute.ProjectID, minute.AppID}
		seen.minutes[key] = append(seen.minutes[key], minute)
	}
	for _, hour := range hours {
		key := appKey{hour.ProjectID, hour.AppID}
		seen.hours[key] = append(seen.hours[key], hour)
	}
	for _, volume := range volumes {
		seen.volumes[appKey{volume.ProjectID, volume.AppID}] = volume
	}
	return seen
}

func (o observations) app(key appKey) Observed {
	return Observed{Minutes: o.minutes[key], Hours: o.hours[key], Totals: o.volumes[key].Totals}
}

// keys is every app the meter has anything for, in a stable order.
func (o observations) keys() []appKey {
	set := map[appKey]bool{}
	for key := range o.minutes {
		set[key] = true
	}
	for key := range o.hours {
		set[key] = true
	}
	for key := range o.volumes {
		set[key] = true
	}
	out := make([]appKey, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].project != out[j].project {
			return out[i].project < out[j].project
		}
		return out[i].app < out[j].app
	})
	return out
}

// snapshot is who the meter said owned an app's traffic, from the rows
// themselves; the last minute (or hour) wins, so a tier change is followed.
func (o observations) snapshot(key appKey) (owner, tier string) {
	if volume, ok := o.volumes[key]; ok {
		owner, tier = volume.OwnerID, volume.Tier
	}
	for _, hour := range o.hours[key] {
		if hour.OwnerID != "" {
			owner, tier = hour.OwnerID, hour.Tier
		}
	}
	for _, minute := range o.minutes[key] {
		if minute.OwnerID != "" {
			owner, tier = minute.OwnerID, minute.Tier
		}
	}
	return owner, tier
}

type bucketKey struct{ owner, tier string }

// bucket is one owner's traffic on one tier: every app it ran there, live or
// deleted, judged together against that tier's reference.
type bucket struct {
	subject Subject
	minutes [][]netmeter.Sample
	hours   [][]netmeter.Hourly
	totals  Totals
	apps    int
}

// attributor puts each app's traffic into its owner's tier bucket. The owner
// and tier come from what the meter stored with the traffic; only traffic
// that predates that snapshot falls back to the live directory, so deleting
// and recreating a project does not reset the evidence.
type attributor struct {
	projects map[string]Subject
	buckets  map[bucketKey]*bucket
}

func newAttributor(projects map[string]Subject) *attributor {
	return &attributor{projects: projects, buckets: map[bucketKey]*bucket{}}
}

// identify says who owns an app's traffic and on what tier; ok is false when
// neither the snapshot nor the directory knows.
func (a *attributor) identify(key appKey, owner, tier string) (string, string, bool) {
	project, known := a.projects[key.project]
	if owner == "" {
		if !known {
			return "", "", false
		}
		return project.OwnerID, strings.ToLower(project.Tier), true
	}
	if tier == "" && known {
		tier = project.Tier
	}
	return owner, strings.ToLower(tier), true
}

// add puts one app's traffic into its bucket. Each app is added once: keys()
// lists an app once however many of its series name it.
func (a *attributor) add(key appKey, seen observations) {
	snapOwner, snapTier := seen.snapshot(key)
	owner, tier, ok := a.identify(key, snapOwner, snapTier)
	if !ok {
		return
	}
	id := bucketKey{owner, tier}
	entry := a.buckets[id]
	if entry == nil {
		entry = &bucket{subject: Subject{OwnerID: owner, Tier: tier}}
		a.buckets[id] = entry
	}
	totals := seen.volumes[key].Totals
	entry.apps++
	entry.minutes = append(entry.minutes, seen.minutes[key])
	entry.hours = append(entry.hours, seen.hours[key])
	entry.totals.TxDay += totals.TxDay
	entry.totals.RxDay += totals.RxDay
	entry.totals.Tx30d += totals.Tx30d
	entry.totals.Rx30d += totals.Rx30d
}

// findings judges every bucket against its own tier's reference, so an
// account's Free apps are never measured on the allowance of its Standard one.
func (a *attributor) findings(cfg Config) []Finding {
	ids := make([]bucketKey, 0, len(a.buckets))
	for id := range a.buckets {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].owner != ids[j].owner {
			return ids[i].owner < ids[j].owner
		}
		return ids[i].tier < ids[j].tier
	})
	var out []Finding
	for _, id := range ids {
		entry := a.buckets[id]
		seen := Observed{Minutes: SumByTimestamp(entry.minutes), Hours: SumHours(entry.hours), Totals: entry.totals}
		for _, finding := range Evaluate(cfg, entry.subject, cfg.Reference(id.tier), seen) {
			finding.Evidence["apps"] = float64(entry.apps)
			out = append(out, finding)
		}
	}
	return out
}
