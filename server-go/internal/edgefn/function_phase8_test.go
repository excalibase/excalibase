package edgefn

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- Phase 8: cronJobs() bundle extraction ---
//
// User bundles import `cronJobs` from @excalibase/server and call
// `.daily(...)` / `.cron(...)` etc. on the returned registry. The
// registry mirrors the table into `globalThis.__excalibase_crons` so the
// bundler can pick it up after esbuild emits the IIFE.
//
// Phase 8 detection is keyed off the `__excalibase_crons` side-channel —
// exactly the pattern Phase 7 uses for `__excalibase_routes`. The
// extracted table lands on Function.CronJobs as a JSON array of
// `{name, schedule, fnRef, args}` rows so the Go-side cron runner can
// schedule them without re-loading the bundle.

// TestBundle_ExtractsCronJobs confirms that a bundle calling cronJobs()
// and registering at least one job populates Function.CronJobs with the
// canonical JSON shape.
func TestBundle_ExtractsCronJobs(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v2crons",
		Name:      "V2 Crons",
		Files: []File{{Path: testIndexTS, Content: `
// User bundle — simulates what @excalibase/server's cronJobs() produces.
const __crons = [
  { name: "daily-digest", schedule: { kind: "daily", hourUTC: 9, minuteUTC: 0 },
    fnRef: { moduleName: "jobs", exportName: "sendDigest" }, args: {} },
  { name: "every-5", schedule: { kind: "cron", expression: "*/5 * * * *" },
    fnRef: { moduleName: "jobs", exportName: "tick" }, args: { n: 1 } },
];
globalThis.__excalibase_crons = __crons;
export default {
  kind: "mutation",
  args: { parse: (a: any) => a },
  handler: async (_ctx: any, _args: any) => null,
  __metadata: { argsJsonSchema: { type: "object", properties: {} } },
};`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if len(fn.CronJobs) == 0 {
		t.Fatalf("CronJobs: empty after Bundle(); expected at least one entry")
	}
	var jobs []map[string]any
	if err := json.Unmarshal(fn.CronJobs, &jobs); err != nil {
		t.Fatalf("unmarshal CronJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("CronJobs: got %d entries, want 2 (%s)", len(jobs), string(fn.CronJobs))
	}
	if got := jobs[0]["name"]; got != "daily-digest" {
		t.Errorf("jobs[0].name: got %q, want %q", got, "daily-digest")
	}
	if got := jobs[1]["name"]; got != "every-5" {
		t.Errorf("jobs[1].name: got %q, want %q", got, "every-5")
	}
}

// TestBundle_NoCronJobs_LeavesNil — a bundle that does NOT call cronJobs()
// must leave Function.CronJobs nil so omitempty keeps the persisted record
// byte-stable for legacy v2 records.
func TestBundle_NoCronJobs_LeavesNil(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v2nocrons",
		Name:      "V2 NoCrons",
		Files: []File{{Path: testIndexTS, Content: `
export default {
  kind: "mutation",
  args: { parse: (a: any) => a },
  handler: async (_ctx: any, _args: any) => null,
  __metadata: { argsJsonSchema: { type: "object", properties: {} } },
};`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if fn.CronJobs != nil {
		t.Errorf("CronJobs: got %s, want nil", string(fn.CronJobs))
	}
}

// TestBundle_CronJobs_StaleClearedOnRedeploy — when a previously-deployed
// function dropped its crons.ts in a new revision, Bundle() must clear
// the stale Function.CronJobs slot (the bundler is the source of truth
// per deploy).
func TestBundle_CronJobs_StaleClearedOnRedeploy(t *testing.T) {
	fn := &Function{
		ProjectID: "proj_test0001",
		ID:        "v2redeploy",
		Name:      "V2 Redeploy",
		CronJobs:  json.RawMessage(`[{"name":"stale","schedule":{"kind":"daily","hourUTC":0,"minuteUTC":0},"fnRef":{"moduleName":"m","exportName":"x"},"args":{}}]`),
		Files: []File{{Path: testIndexTS, Content: `
export default {
  kind: "mutation",
  args: { parse: (a: any) => a },
  handler: async (_ctx: any, _args: any) => null,
  __metadata: { argsJsonSchema: { type: "object", properties: {} } },
};`}},
	}
	if _, err := fn.Bundle(); err != nil {
		t.Fatalf(testBundleFmt, err)
	}
	if fn.CronJobs != nil {
		t.Errorf("CronJobs: got %s after redeploy without crons, want nil", string(fn.CronJobs))
	}
}

// TestBundle_CronJobs_RejectsMalformedRow — a row missing `name` or with an
// unknown schedule kind fails the bundle so a bad table never reaches the
// runtime. Defence-in-depth on top of the lib's `cronJobs.<method>()` guards.
func TestBundle_CronJobs_RejectsMalformedRow(t *testing.T) {
	cases := []struct {
		desc    string
		content string
		want    string
	}{
		{
			desc: "missing name",
			content: `
globalThis.__excalibase_crons = [
  { schedule: { kind: "daily", hourUTC: 9, minuteUTC: 0 },
    fnRef: { moduleName: "m", exportName: "x" }, args: {} },
];
export default { kind: "mutation", args: { parse: (a:any)=>a },
  handler: async () => null, __metadata: {} };`,
			want: "cron job name",
		},
		{
			desc: "unknown schedule kind",
			content: `
globalThis.__excalibase_crons = [
  { name: "j", schedule: { kind: "weekly", dayOfWeekUTC: 0, hourUTC: 0, minuteUTC: 0 },
    fnRef: { moduleName: "m", exportName: "x" }, args: {} },
];
export default { kind: "mutation", args: { parse: (a:any)=>a },
  handler: async () => null, __metadata: {} };`,
			want: "schedule kind",
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			fn := &Function{
				ProjectID: "proj_test0001",
				ID:        "v2cronbad",
				Name:      "V2 CronBad",
				Files:     []File{{Path: testIndexTS, Content: c.content}},
			}
			_, err := fn.Bundle()
			if err == nil {
				t.Fatalf("Bundle: expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Bundle: error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}
