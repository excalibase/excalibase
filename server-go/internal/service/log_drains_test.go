package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

var drainNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// fakeLogReader answers per selector, so a query naming the wrong project
// finds nothing, and records every query.
type fakeLogReader struct {
	lines    map[string][]logdrain.Line
	counts   map[string]int64
	err      error
	queries  []string
	readArgs [][2]int64
}

func (f *fakeLogReader) ReadLines(_ context.Context, selector string, from, to int64, limit int) ([]logdrain.Line, error) {
	f.queries = append(f.queries, selector)
	f.readArgs = append(f.readArgs, [2]int64{from, to})
	if f.err != nil {
		return nil, f.err
	}
	out := []logdrain.Line{}
	for _, line := range f.lines[selector] {
		if line.TS >= from && line.TS < to && len(out) < limit {
			out = append(out, line)
		}
	}
	return out, nil
}

func (f *fakeLogReader) CountLines(_ context.Context, selector string, _, _ int64) (int64, error) {
	return f.counts[selector], nil
}

type drainFixture struct {
	svc       *LogDrainService
	store     *fakestore.LogDrains
	vault     *fakeVault
	reader    *fakeLogReader
	instances map[string]*domain.DatabaseInstance
	now       time.Time
}

func newDrainFixture(t *testing.T) *drainFixture {
	t.Helper()
	f := &drainFixture{
		store:  fakestore.NewLogDrains(),
		vault:  newFakeVault(),
		reader: &fakeLogReader{lines: map[string][]logdrain.Line{}, counts: map[string]int64{}},
		instances: map[string]*domain.DatabaseInstance{
			"proj-a": {ProjectID: "proj-a", Namespace: "org-proj-a", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeK8s},
			"proj-b": {ProjectID: "proj-b", Namespace: "org-proj-b", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeK8s},
			"proj-d": {ProjectID: "proj-d", Namespace: "", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker},
		},
		now: drainNow,
	}
	f.svc = NewLogDrainService(LogDrainDeps{
		Store: f.store,
		Vault: f.vault,
		Instances: func(projectID string) (*domain.DatabaseInstance, error) {
			if inst, ok := f.instances[projectID]; ok {
				return inst, nil
			}
			return nil, ErrProjectNotFound
		},
		Logs: f.reader,
		Now:  func() time.Time { return f.now },
	})
	return f
}

func strPtr(s string) *string { return &s }

func (f *drainFixture) datadog(t *testing.T, projectID string) *domain.LogDrain {
	t.Helper()
	drain, err := f.svc.Create(context.Background(), projectID, "user-1", CreateLogDrainInput{
		Name: "dd", Kind: logdrain.KindDatadog, DatadogSite: "eu1", Secret: "dd-key-" + projectID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return drain
}

func TestLogDrains_CreateKeepsTheKeyInTheVaultOnly(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	if !strings.HasPrefix(drain.ID, "ld_") || drain.CursorNS != drainNow.UnixNano() || !drain.Enabled || drain.Status != domain.LogDrainActive {
		t.Errorf("drain %+v", drain)
	}
	stored := f.store.Items[drain.ID]
	if strings.Contains(fmt.Sprintf("%+v", stored), "dd-key-proj-a") {
		t.Error("the key reached the drain row")
	}
	secret := f.vault.data["projects/proj-a/logdrains/"+drain.ID]
	if secret["secret"] != "dd-key-proj-a" {
		t.Errorf("vault = %v", f.vault.data)
	}
	has, err := f.svc.HasSecret(drain)
	if err != nil || !has {
		t.Errorf("HasSecret = %v, %v", has, err)
	}
}

func TestLogDrains_CreateValidates(t *testing.T) {
	f := newDrainFixture(t)
	cases := map[string]CreateLogDrainInput{
		"unknown kind":         {Name: "x", Kind: "syslog", URL: "https://logs.example.com/"},
		"datadog other site":   {Name: "x", Kind: logdrain.KindDatadog, DatadogSite: "evil.example.com", Secret: "k"},
		"datadog no key":       {Name: "x", Kind: logdrain.KindDatadog, DatadogSite: "us1"},
		"https plain http":     {Name: "x", Kind: logdrain.KindHTTPS, URL: "http://logs.example.com/"},
		"https private":        {Name: "x", Kind: logdrain.KindHTTPS, URL: "https://192.168.1.10/"},
		"https header no key":  {Name: "x", Kind: logdrain.KindHTTPS, URL: "https://logs.example.com/", HeaderName: "Authorization"},
		"https key no header":  {Name: "x", Kind: logdrain.KindHTTPS, URL: "https://logs.example.com/", Secret: "k"},
		"https reserved hdr":   {Name: "x", Kind: logdrain.KindHTTPS, URL: "https://logs.example.com/", HeaderName: "Host", Secret: "k"},
		"no name":              {Name: "", Kind: logdrain.KindHTTPS, URL: "https://logs.example.com/"},
		"secret with newline":  {Name: "x", Kind: logdrain.KindDatadog, DatadogSite: "us1", Secret: "a\r\nb"},
		"datadog carrying url": {Name: "x", Kind: logdrain.KindDatadog, DatadogSite: "us1", Secret: "k", URL: "https://logs.example.com/"},
	}
	for name, input := range cases {
		if _, err := f.svc.Create(context.Background(), "proj-a", "u", input); !errors.Is(err, logdrain.ErrInvalidDrain) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(f.store.Items) != 0 || len(f.vault.data) != 0 {
		t.Errorf("refused drains left state: %v %v", f.store.Items, f.vault.data)
	}
}

func TestLogDrains_AtMostThreePerProject(t *testing.T) {
	f := newDrainFixture(t)
	for range MaxLogDrainsPerProject {
		f.datadog(t, "proj-a")
	}
	_, err := f.svc.Create(context.Background(), "proj-a", "u", CreateLogDrainInput{Name: "4", Kind: logdrain.KindDatadog, DatadogSite: "us1", Secret: "k4"})
	if !errors.Is(err, domain.ErrLogDrainLimit) {
		t.Fatalf("err = %v", err)
	}
	if len(f.vault.data) != MaxLogDrainsPerProject {
		t.Errorf("the refused drain's key stayed in the vault: %d keys", len(f.vault.data))
	}
	f.datadog(t, "proj-b")
}

func TestLogDrains_UnavailableWithoutLogStoreOrOnSingleHost(t *testing.T) {
	f := newDrainFixture(t)
	if _, err := f.svc.Create(context.Background(), "proj-d", "u", CreateLogDrainInput{Name: "x", Kind: logdrain.KindDatadog, DatadogSite: "us1", Secret: "k"}); !errors.Is(err, ErrLogDrainsUnavailable) {
		t.Errorf("docker project: %v", err)
	}
	noLoki := NewLogDrainService(LogDrainDeps{Store: f.store, Vault: f.vault, Instances: f.svc.instances, Now: f.svc.now})
	if _, err := noLoki.Create(context.Background(), "proj-a", "u", CreateLogDrainInput{Name: "x", Kind: logdrain.KindDatadog, DatadogSite: "us1", Secret: "k"}); !errors.Is(err, ErrLogDrainsUnavailable) {
		t.Errorf("no log store: %v", err)
	}
}

func TestLogDrains_UpdateReplacesSecretAndTogglesEnabled(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	ctx := context.Background()
	updated, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{Name: strPtr("renamed"), Secret: strPtr("new-key"), DatadogSite: strPtr("us5")})
	if err != nil || updated.Name != "renamed" || updated.DatadogSite != "us5" {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	if f.vault.data["projects/proj-a/logdrains/"+drain.ID]["secret"] != "new-key" {
		t.Error("secret not replaced")
	}
	// An empty secret keeps the stored one.
	if _, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{Secret: strPtr("")}); err != nil {
		t.Fatal(err)
	}
	if f.vault.data["projects/proj-a/logdrains/"+drain.ID]["secret"] != "new-key" {
		t.Error("an empty secret erased the key")
	}

	off, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{Enabled: boolPtr(false)})
	if err != nil || off.Enabled || off.Status != domain.LogDrainDisabled {
		t.Fatalf("disable = %+v, %v", off, err)
	}
	f.now = drainNow.Add(time.Hour)
	on, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{Enabled: boolPtr(true)})
	if err != nil || !on.Enabled || on.Status != domain.LogDrainActive || on.CursorNS != f.now.UnixNano() || on.ConsecutiveFailures != 0 {
		t.Fatalf("enable = %+v, %v: resumes from now, no backfill", on, err)
	}
	if _, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{DatadogSite: strPtr("nowhere")}); !errors.Is(err, logdrain.ErrInvalidDrain) {
		t.Errorf("bad site: %v", err)
	}
}

func TestLogDrains_HTTPSHeaderRules(t *testing.T) {
	f := newDrainFixture(t)
	ctx := context.Background()
	drain, err := f.svc.Create(ctx, "proj-a", "u", CreateLogDrainInput{Name: "h", Kind: logdrain.KindHTTPS, URL: "https://logs.example.com/in"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{HeaderName: strPtr("Authorization")}); !errors.Is(err, logdrain.ErrInvalidDrain) {
		t.Errorf("header without a value: %v", err)
	}
	if _, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{HeaderName: strPtr("Authorization"), Secret: strPtr("Bearer x")}); err != nil {
		t.Fatal(err)
	}
	cleared, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{HeaderName: strPtr("")})
	if err != nil || cleared.HeaderName != "" {
		t.Fatalf("clear = %+v, %v", cleared, err)
	}
	if entry := f.vault.data["projects/proj-a/logdrains/"+drain.ID]; entry["secret"] != "" || entry["url"] != "https://logs.example.com/in" {
		t.Errorf("clearing the header: vault %v", entry)
	}
}

// A drain is reached only through its own project.
func TestLogDrains_OtherProjectsDrainIsNotFound(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	ctx := context.Background()
	if _, err := f.svc.Update(ctx, "proj-b", drain.ID, UpdateLogDrainInput{Name: strPtr("x")}); !errors.Is(err, domain.ErrLogDrainNotFound) {
		t.Errorf("update: %v", err)
	}
	if err := f.svc.Delete(ctx, "proj-b", drain.ID); !errors.Is(err, domain.ErrLogDrainNotFound) {
		t.Errorf("delete: %v", err)
	}
	listed, _ := f.svc.List(ctx, "proj-b")
	if len(listed) != 0 {
		t.Errorf("listed %v", listed)
	}
	if err := f.svc.Delete(ctx, "proj-a", drain.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.vault.data) != 0 || len(f.store.Items) != 0 {
		t.Errorf("delete left %v %v", f.vault.data, f.store.Items)
	}
}

func lineAt(offset time.Duration, text string) logdrain.Line {
	return logdrain.Line{TS: drainNow.Add(offset).UnixNano(), Line: text}
}

func (f *drainFixture) pull(t *testing.T) []logdrain.Batch {
	t.Helper()
	batches, err := f.svc.Pull(context.Background(), logdrain.MaxDrainsPerPull)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	return batches
}

// Each batch carries only its own project's lines, read with that project's
// own namespace selector.
func TestLogDrains_PullIsolatesProjects(t *testing.T) {
	f := newDrainFixture(t)
	a := f.datadog(t, "proj-a")
	b, err := f.svc.Create(context.Background(), "proj-b", "u", CreateLogDrainInput{Name: "b", Kind: logdrain.KindHTTPS,
		URL: "https://logs.example.com/b", HeaderName: "Authorization", Secret: "Bearer b"})
	if err != nil {
		t.Fatal(err)
	}
	selA := drainLogSelector(f.instances["proj-a"])
	selB := drainLogSelector(f.instances["proj-b"])
	f.reader.lines[selA] = []logdrain.Line{lineAt(time.Second, "a1"), lineAt(2*time.Second, "a2")}
	f.reader.lines[selB] = []logdrain.Line{lineAt(time.Second, "b1")}
	f.now = drainNow.Add(time.Minute)

	batches := f.pull(t)
	if len(batches) != 2 {
		t.Fatalf("batches = %+v", batches)
	}
	byDrain := map[string]logdrain.Batch{}
	for _, batch := range batches {
		byDrain[batch.DrainID] = batch
	}
	gotA, gotB := byDrain[a.ID], byDrain[b.ID]
	if gotA.ProjectID != "proj-a" || len(gotA.Lines) != 2 || gotA.Lines[0].Line != "a1" ||
		gotA.Target != "https://http-intake.logs.datadoghq.eu/api/v2/logs" || gotA.Secret != "dd-key-proj-a" {
		t.Errorf("batch A %+v", gotA)
	}
	if gotB.ProjectID != "proj-b" || len(gotB.Lines) != 1 || gotB.Lines[0].Line != "b1" ||
		gotB.Target != "https://logs.example.com/b" || gotB.HeaderName != "Authorization" || gotB.Secret != "Bearer b" {
		t.Errorf("batch B %+v", gotB)
	}
	for _, selector := range f.reader.queries {
		if selector != selA && selector != selB {
			t.Errorf("unexpected selector %q", selector)
		}
	}
	if gotA.From != drainNow.UnixNano() {
		t.Errorf("From = %d, want the cursor", gotA.From)
	}
}

// The window ends a little before now so lines still being shipped are not
// skipped, and starts just after the cursor so no line is sent twice.
func TestLogDrains_PullWindow(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	f.now = drainNow.Add(time.Minute)
	f.pull(t)
	if len(f.reader.readArgs) != 1 {
		t.Fatalf("reads %v", f.reader.readArgs)
	}
	got := f.reader.readArgs[0]
	if got[0] != drain.CursorNS+1 || got[1] != f.now.Add(-logIngestDelay).UnixNano() {
		t.Errorf("window %v", got)
	}
}

func TestLogDrains_EmptyWindowAdvancesWithoutABatch(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	f.now = drainNow.Add(time.Minute)
	if batches := f.pull(t); len(batches) != 0 {
		t.Fatalf("batches %+v", batches)
	}
	stored := f.store.Items[drain.ID]
	if stored.CursorNS != f.now.Add(-logIngestDelay).UnixNano() || !stored.NextAttemptAt.After(f.now) {
		t.Errorf("drain %+v", stored)
	}
	if again := f.pull(t); len(again) != 0 || len(f.reader.queries) != 1 {
		t.Errorf("an idle drain was queried again at once")
	}
}

// A full batch ends at its last line, so the rest follows in the next one.
// A full batch ends before its last timestamp, so lines sharing that
// timestamp beyond the read come in the next batch rather than being lost.
func TestLogDrains_FullBatchEndsBeforeItsLastTimestamp(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	sel := drainLogSelector(f.instances["proj-a"])
	for i := range logdrain.MaxBatchLines - 2 {
		f.reader.lines[sel] = append(f.reader.lines[sel], lineAt(time.Duration(i+1)*time.Millisecond, "x"))
	}
	shared := lineAt(time.Second, "shared")
	for range 5 {
		f.reader.lines[sel] = append(f.reader.lines[sel], shared)
	}
	f.now = drainNow.Add(time.Minute)
	batches := f.pull(t)
	if len(batches) != 1 || len(batches[0].Lines) != logdrain.MaxBatchLines-2 {
		t.Fatalf("batches %d", len(batches))
	}
	last := batches[0].Lines[len(batches[0].Lines)-1].TS
	if batches[0].To != last || last >= shared.TS || batches[0].From != drain.CursorNS {
		t.Errorf("From %d To %d, last %d", batches[0].From, batches[0].To, last)
	}
}

func TestEndOnWholeTimestamp_KeepsASingleTimestampBatch(t *testing.T) {
	lines := []logdrain.Line{{TS: 5, Line: "a"}, {TS: 5, Line: "b"}}
	if got := endOnWholeTimestamp(lines); len(got) != 2 {
		t.Errorf("got %d lines", len(got))
	}
}

func TestLogDrains_BatchBoundedByBytes(t *testing.T) {
	f := newDrainFixture(t)
	f.datadog(t, "proj-a")
	sel := drainLogSelector(f.instances["proj-a"])
	big := strings.Repeat("y", logdrain.MaxLineBytes)
	for i := range 100 {
		f.reader.lines[sel] = append(f.reader.lines[sel], lineAt(time.Duration(i+1)*time.Millisecond, big))
	}
	f.now = drainNow.Add(time.Minute)
	batches := f.pull(t)
	size := 0
	for _, line := range batches[0].Lines {
		size += len(line.Line)
	}
	if size > logdrain.MaxBatchBytes || len(batches[0].Lines) == 100 {
		t.Errorf("batch of %d lines, %d bytes", len(batches[0].Lines), size)
	}
	if batches[0].To != batches[0].Lines[len(batches[0].Lines)-1].TS {
		t.Error("a cut batch must end at its last line")
	}
}

// A drain far behind skips ahead and counts what it skipped, instead of
// holding an unbounded backlog.
func TestLogDrains_OverflowSkipsAheadAndCounts(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	sel := drainLogSelector(f.instances["proj-a"])
	f.reader.counts[sel] = 12345
	f.now = drainNow.Add(2 * time.Hour)
	f.pull(t)
	stored := f.store.Items[drain.ID]
	if stored.DroppedLines != 12345 {
		t.Errorf("dropped = %d", stored.DroppedLines)
	}
	resumeFrom := f.now.Add(-logIngestDelay - logResumeWindow).UnixNano()
	if got := f.reader.readArgs[0][0]; got != resumeFrom+1 {
		t.Errorf("read from %d, want %d", got, resumeFrom+1)
	}
}

func TestLogDrains_LogStoreErrorDefersTheDrain(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	f.reader.err = errors.New("loki down at http://loki.platform:3100")
	f.now = drainNow.Add(time.Minute)
	if batches := f.pull(t); len(batches) != 0 {
		t.Fatalf("batches %+v", batches)
	}
	stored := f.store.Items[drain.ID]
	if stored.CursorNS != drain.CursorNS || !stored.NextAttemptAt.After(f.now) || stored.ConsecutiveFailures != 0 {
		t.Errorf("drain %+v: a platform outage is not the drain's failure", stored)
	}
}

func (f *drainFixture) ackFor(t *testing.T, batch logdrain.Batch, sendErr string) {
	t.Helper()
	if err := f.svc.Ack(context.Background(), logdrain.Ack{DrainID: batch.DrainID, From: batch.From, To: batch.To, Lines: len(batch.Lines), Error: sendErr}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
}

func TestLogDrains_AckDeliveredMovesTheCursor(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	sel := drainLogSelector(f.instances["proj-a"])
	f.reader.lines[sel] = []logdrain.Line{lineAt(time.Second, "a1")}
	f.now = drainNow.Add(time.Minute)
	batch := f.pull(t)[0]
	f.ackFor(t, batch, "")
	stored := f.store.Items[drain.ID]
	if stored.CursorNS != batch.To || stored.SentLines != 1 || stored.LastSuccessAt == nil || stored.Status != domain.LogDrainActive {
		t.Errorf("drain %+v", stored)
	}
	// A repeated ack for the same batch changes nothing.
	f.ackFor(t, batch, "")
	if f.store.Items[drain.ID].SentLines != 1 {
		t.Error("a stale ack counted twice")
	}
}

func TestLogDrains_FailuresBackOffThenDisable(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	sel := drainLogSelector(f.instances["proj-a"])
	var previousWait time.Duration
	for i := 1; i <= MaxLogDrainFailures; i++ {
		f.now = f.store.Items[drain.ID].NextAttemptAt.Add(time.Second)
		if f.now.Before(drainNow.Add(time.Minute)) {
			f.now = drainNow.Add(time.Minute)
		}
		// Long waits also skip the backlog; a fresh line is always there to send.
		f.reader.lines[sel] = []logdrain.Line{{TS: f.now.Add(-30 * time.Second).UnixNano(), Line: "fresh"}}
		batches := f.pull(t)
		if len(batches) != 1 {
			t.Fatalf("attempt %d: batches %d", i, len(batches))
		}
		f.ackFor(t, batches[0], "the destination answered 403 for key dd-key-proj-a")
		stored := f.store.Items[drain.ID]
		wait := stored.NextAttemptAt.Sub(f.now)
		if stored.ConsecutiveFailures != i || wait < previousWait || wait > maxLogDrainBackoff {
			t.Fatalf("attempt %d: %+v wait %v", i, stored, wait)
		}
		if strings.Contains(stored.LastError, "dd-key-proj-a") || !strings.Contains(stored.LastError, "403") {
			t.Errorf("last error %q", stored.LastError)
		}
		previousWait = wait
	}
	stored := f.store.Items[drain.ID]
	if stored.Enabled || stored.Status != domain.LogDrainDisabled || stored.SentLines != 0 {
		t.Fatalf("after %d failures: %+v", MaxLogDrainFailures, stored)
	}
	f.now = f.now.Add(24 * time.Hour)
	if batches := f.pull(t); len(batches) != 0 {
		t.Error("a disabled drain was pulled")
	}
}

func TestLogDrains_AckRefusesNonsense(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	ctx := context.Background()
	bad := []logdrain.Ack{
		{DrainID: drain.ID, From: drain.CursorNS, To: drain.CursorNS - 1},
		{DrainID: drain.ID, From: drain.CursorNS, To: drain.CursorNS + 1, Lines: logdrain.MaxBatchLines + 1},
		{DrainID: drain.ID, From: drain.CursorNS, To: drainNow.Add(time.Hour).UnixNano()},
		{DrainID: drain.ID, From: drain.CursorNS, To: drain.CursorNS + 1, Lines: -1},
	}
	for _, ack := range bad {
		if err := f.svc.Ack(ctx, ack); !errors.Is(err, ErrInvalidLogDrainAck) {
			t.Errorf("%+v: %v", ack, err)
		}
	}
	if err := f.svc.Ack(ctx, logdrain.Ack{DrainID: "ld_gone", From: 1, To: 2}); err != nil {
		t.Errorf("an ack for a deleted drain is ignored: %v", err)
	}
}

func TestLogDrains_MissingKeyFailsTheDrain(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	delete(f.vault.data, "projects/proj-a/logdrains/"+drain.ID)
	f.now = drainNow.Add(time.Minute)
	if batches := f.pull(t); len(batches) != 0 {
		t.Fatalf("sent without its key: %+v", batches)
	}
	stored := f.store.Items[drain.ID]
	if stored.ConsecutiveFailures != 1 || !strings.Contains(stored.LastError, "key") {
		t.Errorf("drain %+v", stored)
	}
}

func TestLogDrains_PullBoundsDrainsPerCall(t *testing.T) {
	f := newDrainFixture(t)
	for i := range logdrain.MaxDrainsPerPull + 5 {
		id := fmt.Sprintf("ld_%016d", i)
		f.store.Items[id] = domain.LogDrain{ID: id, ProjectID: "proj-a", Kind: logdrain.KindHTTPS, URL: "https://logs.example.com/...",
			Enabled: true, Status: domain.LogDrainActive, CursorNS: drainNow.UnixNano(), NextAttemptAt: drainNow}
		f.vault.data["projects/proj-a/logdrains/"+id] = map[string]string{"url": "https://logs.example.com/in"}
		f.reader.lines[drainLogSelector(f.instances["proj-a"])] = []logdrain.Line{lineAt(time.Second, "x")}
	}
	f.now = drainNow.Add(time.Minute)
	if batches := f.pull(t); len(batches) != logdrain.MaxDrainsPerPull {
		t.Errorf("%d batches", len(batches))
	}
	// Leased drains are not handed out again until their lease ends.
	if again := f.pull(t); len(again) != 5 {
		t.Errorf("second pull %d batches", len(again))
	}
}

func TestLogDrains_UpdateDestination(t *testing.T) {
	f := newDrainFixture(t)
	ctx := context.Background()
	https, err := f.svc.Create(ctx, "proj-a", "u", CreateLogDrainInput{Name: "h", Kind: logdrain.KindHTTPS, URL: "https://logs.example.com/in"})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := f.svc.Update(ctx, "proj-a", https.ID, UpdateLogDrainInput{URL: strPtr("https://Other.Example.com:443/v2")})
	if err != nil || moved.URL != "https://other.example.com/..." || f.vault.data["projects/proj-a/logdrains/"+https.ID]["url"] != "https://other.example.com/v2" {
		t.Fatalf("move = %+v, %v", moved, err)
	}
	if _, err := f.svc.Update(ctx, "proj-a", https.ID, UpdateLogDrainInput{URL: strPtr("https://[::1]/")}); !errors.Is(err, logdrain.ErrInvalidDrain) {
		t.Errorf("loopback url: %v", err)
	}
	datadog := f.datadog(t, "proj-a")
	if _, err := f.svc.Update(ctx, "proj-a", datadog.ID, UpdateLogDrainInput{URL: strPtr("https://logs.example.com/")}); !errors.Is(err, logdrain.ErrInvalidDrain) {
		t.Errorf("url on a datadog drain: %v", err)
	}
	if _, err := f.svc.Update(ctx, "proj-a", datadog.ID, UpdateLogDrainInput{Secret: strPtr("bad\nkey")}); !errors.Is(err, logdrain.ErrInvalidDrain) {
		t.Errorf("bad key: %v", err)
	}
	if _, err := f.svc.Update(ctx, "proj-a", "ld_missing", UpdateLogDrainInput{}); !errors.Is(err, domain.ErrLogDrainNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func TestLogDrainBackoff(t *testing.T) {
	if logDrainBackoff(1) != minLogDrainWait || logDrainBackoff(2) != 2*minLogDrainWait || logDrainBackoff(50) != maxLogDrainBackoff {
		t.Errorf("backoff %v %v %v", logDrainBackoff(1), logDrainBackoff(2), logDrainBackoff(50))
	}
}

func (f *drainFixture) httpsWithHeader(t *testing.T) *domain.LogDrain {
	t.Helper()
	drain, err := f.svc.Create(context.Background(), "proj-a", "u", CreateLogDrainInput{Name: "h", Kind: logdrain.KindHTTPS,
		URL: "https://logs.example.com/receiver/TOKEN123", HeaderName: "Authorization", Secret: "Bearer stored"})
	if err != nil {
		t.Fatal(err)
	}
	return drain
}

// The full URL may carry a token: it stays in the vault, and only scheme and
// host are kept on the drain.
func TestLogDrains_FullURLStaysInTheVault(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.httpsWithHeader(t)
	if drain.URL != "https://logs.example.com/..." || strings.Contains(fmt.Sprintf("%+v", f.store.Items[drain.ID]), "TOKEN123") {
		t.Errorf("drain %+v", f.store.Items[drain.ID])
	}
	if f.vault.data["projects/proj-a/logdrains/"+drain.ID]["url"] != "https://logs.example.com/receiver/TOKEN123" {
		t.Errorf("vault %v", f.vault.data)
	}
	// Sending the shown URL back keeps the stored one.
	if _, err := f.svc.Update(context.Background(), "proj-a", drain.ID, UpdateLogDrainInput{URL: strPtr(drain.URL), Name: strPtr("n")}); err != nil {
		t.Fatal(err)
	}
	if f.vault.data["projects/proj-a/logdrains/"+drain.ID]["url"] != "https://logs.example.com/receiver/TOKEN123" {
		t.Errorf("the shown URL replaced the stored one: %v", f.vault.data)
	}
}

// A stored header value never follows the drain to another host or header
// unless it is entered again.
func TestLogDrains_StoredHeaderValueDoesNotFollowARetarget(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.httpsWithHeader(t)
	ctx := context.Background()
	refused := []UpdateLogDrainInput{
		{URL: strPtr("https://attacker.example.net/collect")},
		{HeaderName: strPtr("X-Api-Key")},
	}
	for _, input := range refused {
		if _, err := f.svc.Update(ctx, "proj-a", drain.ID, input); !errors.Is(err, logdrain.ErrInvalidDrain) {
			t.Errorf("%+v: %v", input, err)
		}
	}
	if got := f.vault.data["projects/proj-a/logdrains/"+drain.ID]["url"]; got != "https://logs.example.com/receiver/TOKEN123" {
		t.Errorf("a refused update moved the endpoint: %s", got)
	}
	if _, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{URL: strPtr("https://logs.example.com/other")}); err != nil {
		t.Errorf("same host, new path: %v", err)
	}
	moved, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{URL: strPtr("https://new.example.net/in"), Secret: strPtr("Bearer new")})
	if err != nil || moved.URL != "https://new.example.net/..." {
		t.Fatalf("retarget with a new value: %+v, %v", moved, err)
	}
	if f.vault.data["projects/proj-a/logdrains/"+drain.ID]["secret"] != "Bearer new" {
		t.Error("new value not stored")
	}
	if _, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{HeaderName: strPtr(""), Secret: strPtr("x")}); !errors.Is(err, logdrain.ErrInvalidDrain) {
		t.Errorf("a value with no header: %v", err)
	}
}

// Postgres drains read the database container only, never the operator's sidecars.
func TestDrainLogSelector_DatabaseContainerOnly(t *testing.T) {
	pg := drainLogSelector(&domain.DatabaseInstance{ProjectID: "p1", Namespace: "o-proj-p1", DBType: domain.PostgreSQL})
	if !strings.HasPrefix(pg, `{namespace="o-proj-p1",cnpg_io_cluster=`) || !strings.HasSuffix(pg, `,container="postgres"}`) {
		t.Errorf("postgres selector %s", pg)
	}
	mysql := drainLogSelector(&domain.DatabaseInstance{ProjectID: "p1", Namespace: "o-proj-p1", DBType: domain.MySQL})
	if !strings.Contains(mysql, "container=") {
		t.Errorf("mysql selector %s", mysql)
	}
}

// Lines full of characters JSON escapes are measured as sent, and one pull
// answer stays within its budget however many drains are due.
func TestLogDrains_PullStaysWithinItsByteBudget(t *testing.T) {
	f := newDrainFixture(t)
	ctx := context.Background()
	sel := drainLogSelector(f.instances["proj-a"])
	escaped := strings.Repeat("\x01", logdrain.MaxLineBytes)
	for i := range 200 {
		f.reader.lines[sel] = append(f.reader.lines[sel], lineAt(time.Duration(i+1)*time.Millisecond, escaped))
	}
	for range MaxLogDrainsPerProject {
		f.datadog(t, "proj-a")
	}
	for i := range logdrain.MaxDrainsPerPull {
		id := fmt.Sprintf("ld_extra%08d", i)
		f.store.Items[id] = domain.LogDrain{ID: id, ProjectID: "proj-a", Kind: logdrain.KindDatadog, DatadogSite: "us1",
			Enabled: true, CursorNS: drainNow.UnixNano(), NextAttemptAt: drainNow}
		f.vault.data["projects/proj-a/logdrains/"+id] = map[string]string{"secret": "k"}
	}
	f.now = drainNow.Add(time.Minute)
	batches, err := f.svc.Pull(ctx, logdrain.MaxDrainsPerPull)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, batch := range batches {
		size := 0
		for _, line := range batch.Lines {
			size += logdrain.EncodedSize(line.Line)
		}
		if size > logdrain.MaxBatchBytes {
			t.Errorf("batch of %d bytes", size)
		}
		total += size
	}
	if total > logdrain.MaxPullBytes || len(batches) == 0 {
		t.Errorf("pull of %d batches, %d bytes", len(batches), total)
	}
}

// Only the leased batch settles a drain; an ack naming another range is ignored.
func TestLogDrains_AckMustMatchTheLease(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	sel := drainLogSelector(f.instances["proj-a"])
	f.reader.lines[sel] = []logdrain.Line{lineAt(time.Second, "a1")}
	f.now = drainNow.Add(time.Minute)
	batch := f.pull(t)[0]
	ctx := context.Background()
	if err := f.svc.Ack(ctx, logdrain.Ack{DrainID: drain.ID, From: batch.From, To: batch.To - 1, Lines: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Ack(ctx, logdrain.Ack{DrainID: drain.ID, From: batch.From, To: batch.To, Lines: 400}); err != nil {
		t.Fatal(err)
	}
	if stored := f.store.Items[drain.ID]; stored.CursorNS != drain.CursorNS || stored.SentLines != 0 {
		t.Errorf("a mismatched ack moved the drain: %+v", stored)
	}
	// An Admin's edit while the batch is out leaves its lease alone.
	if _, err := f.svc.Update(ctx, "proj-a", drain.ID, UpdateLogDrainInput{Name: strPtr("renamed")}); err != nil {
		t.Fatal(err)
	}
	f.ackFor(t, batch, "")
	if stored := f.store.Items[drain.ID]; stored.CursorNS != batch.To || stored.SentLines != 1 || stored.Name != "renamed" {
		t.Errorf("after ack %+v", stored)
	}
}

func TestLogDrains_NewDrainWaitsForItsFirstWindow(t *testing.T) {
	f := newDrainFixture(t)
	drain := f.datadog(t, "proj-a")
	if batches := f.pull(t); len(batches) != 0 {
		t.Fatalf("batches %+v", batches)
	}
	if next := f.store.Items[drain.ID].NextAttemptAt; !next.After(drainNow.Add(logIngestDelay)) {
		t.Errorf("next attempt %v: a drain with nothing to read yet is not due", next)
	}
}
