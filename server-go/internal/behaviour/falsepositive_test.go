package behaviour

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

// The false-positive set: what ordinary apps look like, built from metadata
// only (rates and ratios). None of these may be reviewed.
func TestEvaluate_OrdinaryAppsAreNotFlagged(t *testing.T) {
	cfg := testConfig()
	batch := series(60, 5, 0.1)
	for i := 0; i < 15; i++ { // a nightly job: 15 minutes flat out, then quiet
		batch[i].TxBps, batch[i].RxBps = freeRef*0.95, freeRef*0.05
	}
	cases := map[string][]netmeter.Sample{
		"a CDN-like static site, tx heavy at 40% of the reference": series(60, 40, 0.02),
		"an upload app, rx heavy":                                  series(60, 30, 25),
		"a batch job with a short burst":                           batch,
		"a chat websocket, 1:1 but tiny":                           series(60, 3, 1.0),
		"a downloader, rx heavy, little tx":                        series(60, 5, 60),
		"an idle app":                                              series(60, 0, 0),
	}
	for name, minutes := range cases {
		if got := Evaluate(cfg, app("free"), freeRef, Observed{Minutes: minutes, Totals: Totals{TxDay: freeRef * 86400 * 0.05, Tx30d: freeRef * 86400 * 30 * 0.02}}); len(got) != 0 {
			t.Errorf("%s was flagged: %+v", name, got)
		}
	}
}

// The incident shapes, built from the figures seen in the admin console and the
// disguised proxies' behaviour (synthetic series, no customer image or payload).
func TestEvaluate_TheKnownIncidentShapesAreFlagged(t *testing.T) {
	cfg := testConfig()
	cases := map[string]struct {
		minutes []netmeter.Sample
		want    string
	}{
		"ws-relay: a steady 1:1 relay at 60% of the reference": {series(60, 60, 1.0), SignalRelaySymmetry},
		"fleet-node: 1.1:1 at 85%":                             {series(60, 85, 1.1), SignalSustainedEgress},
		"hy2x: tx heavy at 95%, still sustained":               {series(60, 95, 0.3), SignalSustainedEgress},
	}
	for name, tc := range cases {
		if got := signals(Evaluate(cfg, app("free"), freeRef, Observed{Minutes: tc.minutes, Totals: Totals{}})); got[tc.want].Signal == "" {
			t.Errorf("%s: %s not raised: %+v", name, tc.want, got)
		}
	}
}
