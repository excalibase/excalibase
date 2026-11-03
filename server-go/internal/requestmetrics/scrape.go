package requestmetrics

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// maxScrapeBytes bounds one pod's answer: about a thousand projects' series.
const maxScrapeBytes = 8 << 20

const scrapeTimeout = 5 * time.Second

// Resolver finds every pod address behind a headless Service name.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Scraper reads every pod of each target, a headless Service's host:port.
type Scraper struct {
	targets  []string
	resolver Resolver
	client   *http.Client
}

func NewScraper(targets []string, resolver Resolver) *Scraper {
	return &Scraper{
		targets:  append([]string{}, targets...),
		resolver: resolver,
		client: &http.Client{
			Timeout: scrapeTimeout,
			// The internal port answers directly; a redirect is never followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Scrape answers the readings it could take and an error for each pod or
// target it could not; one dead pod does not hide the others.
func (s *Scraper) Scrape(ctx context.Context) ([]Reading, []error) {
	var readings []Reading
	var failures []error
	for _, target := range s.targets {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			failures = append(failures, fmt.Errorf("target %q: %w", target, err))
			continue
		}
		addresses, err := s.resolver.LookupHost(ctx, host)
		if err != nil {
			failures = append(failures, fmt.Errorf("resolve %s: %w", host, err))
			continue
		}
		for _, address := range addresses {
			reading, err := s.read(ctx, net.JoinHostPort(address, port))
			if err != nil {
				failures = append(failures, fmt.Errorf("%s (%s): %w", host, address, err))
				continue
			}
			readings = append(readings, reading)
		}
	}
	return readings, failures
}

func (s *Scraper) read(ctx context.Context, address string) (Reading, error) {
	// The counters port is cluster-internal and admitted to provisioning only by NetworkPolicy.
	target := url.URL{Scheme: "http", Host: address, Path: "/metrics"}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Reading{}, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return Reading{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Reading{}, fmt.Errorf("answered %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxScrapeBytes+1))
	if err != nil {
		return Reading{}, err
	}
	if len(body) > maxScrapeBytes {
		return Reading{}, fmt.Errorf("answer larger than %d bytes", maxScrapeBytes)
	}
	return Parse(string(body))
}
