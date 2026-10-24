package requestmetrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	addresses, ok := f[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	return addresses, nil
}

func serveScrape(t *testing.T, body string, calls *[]string) (string, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	return host, port
}

func TestScraper_ReadsEveryPodBehindEachTarget(t *testing.T) {
	var calls []string
	hostA, port := serveScrape(t, strings.Replace(authScrape, "aaaa", "pod-a", 1), &calls)
	scraper := NewScraper([]string{"graphql-request-metrics:" + port}, fakeResolver{"graphql-request-metrics": {hostA, hostA}})

	readings, failures := scraper.Scrape(context.Background())
	if len(failures) != 0 {
		t.Fatalf("failures: %v", failures)
	}
	if len(readings) != 2 || len(calls) != 2 || calls[0] != "GET /metrics" {
		t.Errorf("every address is scraped at /metrics: %d readings, calls %v", len(readings), calls)
	}
}

func TestScraper_AnUnreachablePodIsReportedAndTheRestStillRead(t *testing.T) {
	var calls []string
	host, port := serveScrape(t, authScrape, &calls)
	scraper := NewScraper([]string{"auth-request-metrics:" + port, "gone:9464"}, fakeResolver{"auth-request-metrics": {host, "127.0.0.2"}})

	readings, failures := scraper.Scrape(context.Background())
	if len(readings) != 1 {
		t.Errorf("the reachable pod is read: %d", len(readings))
	}
	if len(failures) != 2 {
		t.Errorf("the dead address and the unresolvable target are reported: %v", failures)
	}
}

func TestScraper_RefusesAnOversizedAnswer(t *testing.T) {
	var calls []string
	host, port := serveScrape(t, authScrape+strings.Repeat("# padding\n", maxScrapeBytes/10+1), &calls)
	scraper := NewScraper([]string{"auth:" + port}, fakeResolver{"auth": {host}})
	if readings, failures := scraper.Scrape(context.Background()); len(readings) != 0 || len(failures) != 1 {
		t.Errorf("an answer past the cap is refused: %d readings, %v", len(readings), failures)
	}
}

func TestScraper_NoTargetsReadsNothing(t *testing.T) {
	readings, failures := NewScraper(nil, fakeResolver{}).Scrape(context.Background())
	if len(readings) != 0 || len(failures) != 0 {
		t.Errorf("nothing configured, nothing read")
	}
}
