package sites

import (
	"context"
	"errors"
	"log"
)

// SweepReport is what one pass over the waiting custom hostnames did.
type SweepReport struct {
	Checked   int
	Activated int
	Expired   int
	Failed    int
}

// RefreshPendingHostnames checks the hostnames that are not serving, so one
// goes live without its owner coming back to Studio, and drops those that
// have not served for PendingHostnameLifetime. A site busy with a deploy is
// skipped until the next pass.
func (s *Service) RefreshPendingHostnames(ctx context.Context) SweepReport {
	report := SweepReport{}
	if !s.CustomDomainsAvailable() {
		return report
	}
	pending, err := s.options.Hostnames.PendingHostnames(ctx, pendingSweepBatch)
	if err != nil {
		log.Printf("sites: list waiting custom domains: %v", err)
		report.Failed++
		return report
	}
	for _, listed := range pending {
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

// sweepOne acts only on the hostname as it is once the site's lease is held:
// a removal or a re-add may have happened since it was listed.
func (s *Service) sweepOne(ctx context.Context, listed *CustomHostname, report *SweepReport) {
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
	site, hostname, err := s.lookupHostname(ctx, listed.ProjectID, listed.SiteID, listed.ID)
	if errors.Is(err, ErrHostnameNotFound) || errors.Is(err, ErrSiteNotFound) {
		return
	}
	if err != nil {
		log.Printf("sites: custom domain %s: %v", listed.Hostname, err)
		report.Failed++
		return
	}
	if hostname.Status == HostnameActive {
		return
	}
	if s.expired(hostname) {
		s.expire(ctx, site, hostname, report)
		return
	}
	report.Checked++
	refreshed, err := s.refreshHeld(ctx, site, hostname)
	if err != nil {
		report.Failed++
		return
	}
	if refreshed.Status == HostnameActive {
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
