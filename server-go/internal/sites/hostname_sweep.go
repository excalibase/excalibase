package sites

import (
	"context"
	"errors"
	"log"
)

// SweepReport is what one pass over the custom hostnames did.
type SweepReport struct {
	Checked   int
	Activated int
	Expired   int
	Failed    int
}

// RefreshPendingHostnames checks the hostnames that are not serving, so one
// goes live without its owner coming back to Studio, and every serving one
// once per ActiveHostnameRecheckInterval, so a lost proof, a moved domain or
// a lapsed certificate takes it off the router. Hostnames that have not
// served for PendingHostnameLifetime are dropped. A project or site busy with
// another operation is skipped until the next pass.
func (s *Service) RefreshPendingHostnames(ctx context.Context) SweepReport {
	report := SweepReport{}
	if !s.CustomDomainsAvailable() {
		return report
	}
	due, err := s.options.Hostnames.DueHostnames(ctx, s.now().Add(-ActiveHostnameRecheckInterval), pendingSweepBatch)
	if err != nil {
		log.Printf("sites: list custom domains due a check: %v", err)
		report.Failed++
		return report
	}
	for _, listed := range due {
		if !s.expired(listed) && s.now().Sub(listed.CheckedAt) < hostnameRecheckInterval {
			continue
		}
		s.sweepOne(ctx, listed, &report)
	}
	return report
}

func (s *Service) expired(hostname *CustomHostname) bool {
	return hostname.Status != HostnameActive && hostname.InactiveSince != nil &&
		s.now().Sub(*hostname.InactiveSince) > PendingHostnameLifetime
}

// sweepOne acts only on the hostname as it is once the project's lifecycle
// lease and the site's lease are held: a removal or a deletion may have happened since it was listed.
func (s *Service) sweepOne(ctx context.Context, listed *CustomHostname, report *SweepReport) {
	release, err := s.holdProject(ctx, listed.ProjectID)
	if errors.Is(err, ErrSiteBusy) {
		return
	}
	if err != nil {
		log.Printf("sites: custom domain %s: %v", listed.Hostname, err)
		report.Failed++
		return
	}
	defer release()
	site, err := s.mustGet(ctx, listed.ProjectID, listed.SiteID)
	if err != nil {
		log.Printf("sites: custom domain %s: read its site: %v", listed.Hostname, err)
		report.Failed++
		return
	}
	lease, err := s.claim(ctx, site)
	if errors.Is(err, ErrSiteBusy) {
		return
	}
	if err != nil {
		log.Printf("sites: custom domain %s: %v", listed.Hostname, err)
		report.Failed++
		return
	}
	defer lease.release()
	s.sweepHeld(ctx, listed, report)
}

func (s *Service) sweepHeld(ctx context.Context, listed *CustomHostname, report *SweepReport) {
	site, hostname, err := s.lookupHostname(ctx, listed.ProjectID, listed.SiteID, listed.ID)
	if errors.Is(err, ErrHostnameNotFound) || errors.Is(err, ErrSiteNotFound) {
		return
	}
	if err != nil {
		log.Printf("sites: custom domain %s: %v", listed.Hostname, err)
		report.Failed++
		return
	}
	if s.expired(hostname) {
		s.expire(ctx, site, hostname, report)
		return
	}
	report.Checked++
	refreshed, err := s.refreshHeld(ctx, site, hostname, s.routingFor(site))
	if err != nil {
		report.Failed++
		return
	}
	if hostname.Status != HostnameActive && refreshed.Status == HostnameActive {
		report.Activated++
	}
}

func (s *Service) expire(ctx context.Context, site *Site, hostname *CustomHostname, report *SweepReport) {
	if err := s.removeHostname(ctx, site, hostname); err != nil {
		log.Printf("sites: drop custom domain %s that has not served for %s: %v", hostname.Hostname, PendingHostnameLifetime, err)
		report.Failed++
		return
	}
	log.Printf("sites: dropped custom domain %s of site %s: it has not served for %s", hostname.Hostname, site.ID, PendingHostnameLifetime)
	report.Expired++
}
