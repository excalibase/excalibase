package sites

import (
	"context"
	"errors"
	"fmt"
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

// checkOwnership looks for the owner's TXT proof until it is seen once.
// Cloudflare activates a hostname on its CNAME alone, so a CNAME an earlier
// owner left behind would otherwise hand the domain to whoever adds it next.
func (s *Service) checkOwnership(ctx context.Context, hostname *CustomHostname) {
	if hostname.VerifiedAt != nil {
		return
	}
	name, want := VerificationName(hostname.Hostname), verificationPrefix+hostname.VerificationToken
	lookupCtx, cancel := context.WithTimeout(ctx, ownershipLookupTimeout)
	defer cancel()
	values, err := s.options.CustomDomains.Resolver.LookupTXT(lookupCtx, name)
	if err == nil && slices.Contains(values, want) {
		verifiedAt := s.now().UTC()
		hostname.VerifiedAt = &verifiedAt
		return
	}
	var dnsErr *net.DNSError
	if err != nil && (!errors.As(err, &dnsErr) || !dnsErr.IsNotFound) {
		hostname.Errors = append(hostname.Errors, fmt.Sprintf("the TXT record %s could not be looked up yet; it is tried again", name))
		return
	}
	hostname.Errors = append(hostname.Errors, fmt.Sprintf("add the TXT record %s with the value %s to prove the domain is yours", name, want))
}
