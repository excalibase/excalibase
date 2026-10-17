package sites

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"time"
)

// ownershipLookupTimeout bounds one TXT lookup of the owner's proof.
const ownershipLookupTimeout = 10 * time.Second

// TXTResolver looks TXT records up in public DNS; *net.Resolver is one.
type TXTResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// proofState is what one lookup of the owner's TXT proof found.
type proofState int

const (
	// proofUnknown is a lookup that failed for another reason than a missing record: nothing changes.
	proofUnknown proofState = iota
	proofSeen
	proofGone
)

// lookupProof reads the owner's TXT proof. Cloudflare activates a hostname on
// its CNAME alone, so a CNAME an earlier owner left behind would otherwise
// hand the domain to whoever adds it next. The name is fully qualified, so no
// search domain is appended to it.
func (s *Service) lookupProof(ctx context.Context, hostname *CustomHostname) proofState {
	lookupCtx, cancel := context.WithTimeout(ctx, ownershipLookupTimeout)
	defer cancel()
	values, err := s.options.CustomDomains.Resolver.LookupTXT(lookupCtx, VerificationName(hostname.Hostname)+".")
	if err == nil {
		if slices.Contains(values, verificationPrefix+hostname.VerificationToken) {
			return proofSeen
		}
		return proofGone
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return proofGone
	}
	log.Printf("site %s: look up the proof of %s: %v", hostname.SiteID, hostname.Hostname, err)
	return proofUnknown
}

// proofMissing tells the owner what an unproven claim still needs.
func proofMissing(hostname *CustomHostname, state proofState) string {
	name := VerificationName(hostname.Hostname)
	if state == proofUnknown {
		return fmt.Sprintf("the TXT record %s could not be looked up yet; it is tried again", name)
	}
	return fmt.Sprintf("add the TXT record %s with the value %s%s to prove the domain is yours; nothing is set up until it is seen",
		name, verificationPrefix, hostname.VerificationToken)
}
