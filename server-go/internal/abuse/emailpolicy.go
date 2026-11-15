package abuse

import (
	"context"
	_ "embed"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed data/disposable_email_blocklist.conf
var vendoredDisposable string

//go:embed data/disposable_extra.txt
var extraDisposable string

//go:embed data/temp_mx_hosts.txt
var tempMXHosts string

var (
	disposableDomains, disposablePrefixes = parseDomainList(vendoredDisposable, extraDisposable)
	tempMailHosts, _                      = parseDomainList(tempMXHosts)
)

func parseDomainList(lists ...string) (map[string]bool, []string) {
	exact := map[string]bool{}
	var prefixes []string
	for _, list := range lists {
		for _, line := range strings.Split(list, "\n") {
			line = strings.ToLower(strings.TrimSpace(line))
			switch {
			case line == "" || strings.HasPrefix(line, "#"):
			case strings.HasSuffix(line, "*"):
				prefixes = append(prefixes, strings.TrimSuffix(line, "*"))
			default:
				exact[line] = true
			}
		}
	}
	return exact, prefixes
}

func normaliseHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// inSuffixSet reports whether host or any parent domain of it is in set.
func inSuffixSet(set map[string]bool, host string) bool {
	for name := normaliseHost(host); name != ""; {
		if set[name] {
			return true
		}
		dot := strings.IndexByte(name, '.')
		if dot < 0 {
			return false
		}
		name = name[dot+1:]
	}
	return false
}

// DisposableDomain reports whether a mail domain, or a parent of it, is a throwaway provider.
func DisposableDomain(domain string) bool {
	if inSuffixSet(disposableDomains, domain) {
		return true
	}
	for _, label := range strings.Split(normaliseHost(domain), ".") {
		for _, prefix := range disposablePrefixes {
			if strings.HasPrefix(label, prefix) {
				return true
			}
		}
	}
	return false
}

// TempMailHost reports whether a mail server host belongs to a throwaway provider.
func TempMailHost(host string) bool { return inSuffixSet(tempMailHosts, host) }

// ErrEmailRefused is what every refusal of an address matches.
var ErrEmailRefused = errors.New("email address refused")

// RefusalKind says why an address was refused.
type RefusalKind int

const (
	KindInvalid RefusalKind = iota
	KindDisposable
	KindNoMail
	// KindTryAgain is a failed check: it is refused, never allowed, so a DNS outage is no bypass.
	KindTryAgain
)

// EmailRefusal is a refused address with the HTTP answer for it.
type EmailRefusal struct{ Kind RefusalKind }

func (e *EmailRefusal) Error() string {
	switch e.Kind {
	case KindDisposable:
		return "temporary or disposable email addresses are not accepted; use a permanent address"
	case KindNoMail:
		return "that email domain cannot receive mail; use a permanent address"
	case KindTryAgain:
		return "we could not check that email domain right now; try again in a minute"
	default:
		return "invalid email address"
	}
}

func (e *EmailRefusal) Unwrap() error { return ErrEmailRefused }

// Status is 503 for a failed check and 400 otherwise.
func (e *EmailRefusal) Status() int {
	if e.Kind == KindTryAgain {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}

// Resolver is the slice of net.Resolver the policy asks, so tests answer without a network.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// DNSTimeout bounds the whole check of one address.
const DNSTimeout = 3 * time.Second

// EmailPolicy refuses throwaway mail: the lists first, then the domain's mail servers.
type EmailPolicy struct {
	dns Resolver
}

// NewEmailPolicy: a nil resolver uses the system one.
func NewEmailPolicy(dns Resolver) *EmailPolicy {
	if dns == nil {
		dns = net.DefaultResolver
	}
	return &EmailPolicy{dns: dns}
}

// Check returns nil for an address that may sign up, and an *EmailRefusal otherwise.
func (p *EmailPolicy) Check(ctx context.Context, address string) error {
	at := strings.LastIndexByte(address, '@')
	local, domain := "", ""
	if at >= 0 {
		local, domain = address[:at], normaliseHost(address[at+1:])
	}
	if local == "" || domain == "" || strings.ContainsAny(local, "@ ") || strings.Contains(domain, "@") {
		return &EmailRefusal{Kind: KindInvalid}
	}
	if DisposableDomain(domain) {
		return &EmailRefusal{Kind: KindDisposable}
	}
	ctx, cancel := context.WithTimeout(ctx, DNSTimeout)
	defer cancel()
	return p.checkMailServers(ctx, domain)
}

func (p *EmailPolicy) checkMailServers(ctx context.Context, domain string) error {
	records, err := p.dns.LookupMX(ctx, domain)
	if err != nil && !isNotFound(err) {
		return &EmailRefusal{Kind: KindTryAgain}
	}
	if err != nil || len(records) == 0 {
		return p.checkAddressRecord(ctx, domain)
	}
	usable := false
	for _, record := range records {
		host := normaliseHost(record.Host)
		if host == "" {
			continue
		}
		if TempMailHost(host) {
			return &EmailRefusal{Kind: KindDisposable}
		}
		usable = true
	}
	if !usable {
		return &EmailRefusal{Kind: KindNoMail}
	}
	return nil
}

// checkAddressRecord is the implicit MX of a domain with no MX records.
func (p *EmailPolicy) checkAddressRecord(ctx context.Context, domain string) error {
	addresses, err := p.dns.LookupHost(ctx, domain)
	if err != nil && !isNotFound(err) {
		return &EmailRefusal{Kind: KindTryAgain}
	}
	if err != nil || len(addresses) == 0 {
		return &EmailRefusal{Kind: KindNoMail}
	}
	return nil
}

func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}
