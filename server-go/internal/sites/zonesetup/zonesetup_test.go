package zonesetup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

// fakeZone is the user-content zone as the setup sees it.
type fakeZone struct {
	records  []cfworkers.DNSRecord
	fallback cfworkers.FallbackOrigin
	routes   []cfworkers.WorkerRoute
	writes   []string
	failSet  error
}

func (f *fakeZone) DNSRecords(_ context.Context, name string) ([]cfworkers.DNSRecord, error) {
	out := []cfworkers.DNSRecord{}
	for _, record := range f.records {
		if record.Name == name {
			out = append(out, record)
		}
	}
	return out, nil
}

func (f *fakeZone) CreateDNSRecord(_ context.Context, record cfworkers.DNSRecord) (cfworkers.DNSRecord, error) {
	f.writes = append(f.writes, "dns "+record.Type+" "+record.Name+" "+record.Content)
	f.records = append(f.records, record)
	return record, nil
}

func (f *fakeZone) FallbackOrigin(context.Context) (cfworkers.FallbackOrigin, error) {
	return f.fallback, nil
}

func (f *fakeZone) SetFallbackOrigin(_ context.Context, origin string) (cfworkers.FallbackOrigin, error) {
	if f.failSet != nil {
		return cfworkers.FallbackOrigin{}, f.failSet
	}
	f.writes = append(f.writes, "fallback "+origin)
	f.fallback = cfworkers.FallbackOrigin{Origin: origin, Status: "pending_deployment"}
	return f.fallback, nil
}

func (f *fakeZone) WorkerRoutes(context.Context) ([]cfworkers.WorkerRoute, error) {
	return f.routes, nil
}

func (f *fakeZone) CreateWorkerRoute(_ context.Context, pattern, script string) (cfworkers.WorkerRoute, error) {
	f.writes = append(f.writes, "route "+pattern+" "+script)
	route := cfworkers.WorkerRoute{Pattern: pattern, Script: script}
	f.routes = append(f.routes, route)
	return route, nil
}

func plan(apply bool) Plan {
	return Plan{BaseDomain: "excalibase.app", Origin: "cname.excalibase.app", RouterScript: "excalibase-sites-router", Apply: apply}
}

func TestADryRunWritesNothingAndSaysWhatItWouldDo(t *testing.T) {
	zone := &fakeZone{}
	var out bytes.Buffer
	if err := Run(context.Background(), zone, plan(false), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(zone.writes) != 0 {
		t.Fatalf("a dry run wrote %v", zone.writes)
	}
	for _, want := range []string{"would create", "would set the fallback origin", "would route */*", "--apply"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestApplySetsUpAnEmptyZone(t *testing.T) {
	zone := &fakeZone{routes: []cfworkers.WorkerRoute{{Pattern: "*.excalibase.app/*", Script: "excalibase-sites-router"}}}
	var out bytes.Buffer
	if err := Run(context.Background(), zone, plan(true), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "dns AAAA cname.excalibase.app 100::,fallback cname.excalibase.app,route */* excalibase-sites-router"
	if strings.Join(zone.writes, ",") != want {
		t.Fatalf("writes = %v", zone.writes)
	}
	if !zone.records[0].Proxied {
		t.Fatal("the origin record must be proxied")
	}
}

// Run twice, the second pass finds everything in place and writes nothing.
func TestApplyIsIdempotent(t *testing.T) {
	zone := &fakeZone{}
	if err := Run(context.Background(), zone, plan(true), &bytes.Buffer{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	zone.writes = nil
	var out bytes.Buffer
	if err := Run(context.Background(), zone, plan(true), &out); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(zone.writes) != 0 {
		t.Fatalf("second pass wrote %v", zone.writes)
	}
	if strings.Count(out.String(), "already") != 3 {
		t.Errorf("output:\n%s", out.String())
	}
}

// What another setup put there is never overwritten; the operator decides.
func TestApplyRefusesToReplaceSomethingElse(t *testing.T) {
	cases := map[string]*fakeZone{
		"record":    {records: []cfworkers.DNSRecord{{Type: "CNAME", Name: "cname.excalibase.app", Content: "elsewhere.example.com", Proxied: true}}},
		"unproxied": {records: []cfworkers.DNSRecord{{Type: "AAAA", Name: "cname.excalibase.app", Content: "100::"}}},
		"fallback":  {fallback: cfworkers.FallbackOrigin{Origin: "other.excalibase.app", Status: "active"}},
		"route":     {routes: []cfworkers.WorkerRoute{{Pattern: "*/*", Script: "another-worker"}}},
	}
	for name, zone := range cases {
		err := Run(context.Background(), zone, plan(true), &bytes.Buffer{})
		if err == nil {
			t.Errorf("%s: replaced without asking", name)
		}
		for _, write := range zone.writes {
			if strings.HasPrefix(write, map[string]string{"record": "dns", "unproxied": "dns", "fallback": "fallback", "route": "route"}[name]) {
				t.Errorf("%s: wrote %s", name, write)
			}
		}
	}
}

func TestACloudflareRefusalStopsTheSetup(t *testing.T) {
	zone := &fakeZone{failSet: errors.New("cloudflare for SaaS is not enabled on this zone")}
	if err := Run(context.Background(), zone, plan(true), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "fallback origin") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(zone.writes, ",") != "dns AAAA cname.excalibase.app 100::" {
		t.Fatalf("writes = %v, want the route left alone after the failure", zone.writes)
	}
}

func TestPlanRefusesAnOriginOutsideTheZoneAndABadScript(t *testing.T) {
	for _, bad := range []Plan{
		{BaseDomain: "excalibase.app", Origin: "cname.example.com", RouterScript: "excalibase-sites-router"},
		{BaseDomain: "excalibase.app", Origin: "excalibase.app", RouterScript: "excalibase-sites-router"},
		{BaseDomain: "excalibase.app", Origin: "cname.excalibase.app", RouterScript: "../x"},
	} {
		if err := Run(context.Background(), &fakeZone{}, bad, &bytes.Buffer{}); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
