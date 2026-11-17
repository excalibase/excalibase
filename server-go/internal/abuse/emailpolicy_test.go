package abuse

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// fakeDNS answers from tables; a missing name is "not found".
type fakeDNS struct {
	mx        map[string][]string
	hosts     map[string][]string
	mxErr     map[string]error
	hostErr   map[string]error
	deadlines []time.Duration
	asked     []string
}

func (f *fakeDNS) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	f.asked = append(f.asked, "mx:"+name)
	if deadline, ok := ctx.Deadline(); ok {
		f.deadlines = append(f.deadlines, time.Until(deadline))
	}
	if err := f.mxErr[name]; err != nil {
		return nil, err
	}
	hosts, ok := f.mx[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	out := make([]*net.MX, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, &net.MX{Host: host, Pref: 10})
	}
	return out, nil
}

func (f *fakeDNS) LookupHost(_ context.Context, name string) ([]string, error) {
	f.asked = append(f.asked, "a:"+name)
	if err := f.hostErr[name]; err != nil {
		return nil, err
	}
	if addrs, ok := f.hosts[name]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func newDNS() *fakeDNS {
	return &fakeDNS{
		mx: map[string][]string{
			"gmail.com":        {"gmail-smtp-in.l.google.com."},
			"acme.example":     {"mx1.acme.example.", "mx2.acme.example."},
			"fresh-throw.test": {"in.mail.tm."},
			"sneaky.test":      {"relay.example.", "mail.guerrillamail.com."},
			"nullmx.test":      {"."},
		},
		hosts: map[string][]string{"nomx-but-a.test": {"192.0.2.10"}},
		mxErr: map[string]error{
			"slow.test": &net.DNSError{Err: "i/o timeout", Name: "slow.test", IsTimeout: true},
			"fail.test": &net.DNSError{Err: "server misbehaving", Name: "fail.test", IsTemporary: true},
		},
	}
}

func statusOf(err error) int {
	var refusal *EmailRefusal
	if errors.As(err, &refusal) {
		return refusal.Status()
	}
	return 0
}

func TestEmailPolicy_AllowsRealMailDomains(t *testing.T) {
	policy := NewEmailPolicy(newDNS())
	for _, address := range []string{"someone@gmail.com", "Someone@GMAIL.com", "dev@acme.example", "a@nomx-but-a.test"} {
		if err := policy.Check(context.Background(), address); err != nil {
			t.Errorf("%s refused: %v", address, err)
		}
	}
}

func TestEmailPolicy_RefusesListedDomainsWithoutAskingDNS(t *testing.T) {
	dns := newDNS()
	policy := NewEmailPolicy(dns)
	for _, address := range []string{
		"a@fldubai.com", "a@sharklasers.com", "a@guerrillamail.com", "a@guerrillamailblock.com", "a@guerrillamail.de",
		"a@tanpony.com", "a@macsnipe.com", "a@en2ch.com", "a@bankwen.com", "a@javafs.com",
		"a@yuexinmail.edu.kg", "a@nimbusmono.com", "a@tinkmail.me",
		"a@FLDUBAI.COM", "a@box.fldubai.com", "a@deep.sub.tanpony.com", "a@fldubai.com.",
		"a@guerrillamail-new-domain.org",
		"x@euploading.com", "x@sub.euploading.com", "x@EUPLOADING.COM",
	} {
		err := policy.Check(context.Background(), address)
		if !errors.Is(err, ErrEmailRefused) || statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s: err = %v (status %d), want a 400 refusal", address, err, statusOf(err))
		}
	}
	if len(dns.asked) != 0 {
		t.Errorf("a listed domain still cost DNS lookups: %v", dns.asked)
	}
}

func TestEmailPolicy_DoesNotMatchLookalikesOfListedDomains(t *testing.T) {
	policy := NewEmailPolicy(newDNS())
	// notfldubai.com is a different domain; only subdomains of a listed one match.
	for _, address := range []string{"a@notfldubai.com", "x@euploading.co", "x@euploading.com.au", "x@neweuploading.com"} {
		err := policy.Check(context.Background(), address)
		if errors.Is(err, ErrEmailRefused) && err.(*EmailRefusal).Kind == KindDisposable {
			t.Errorf("%s matched the disposable list", address)
		}
	}
}

func TestEmailPolicy_RefusesADomainWhoseMailServerIsATempMailProvider(t *testing.T) {
	policy := NewEmailPolicy(newDNS())
	for _, address := range []string{"a@fresh-throw.test", "a@sneaky.test"} {
		err := policy.Check(context.Background(), address)
		var refusal *EmailRefusal
		if !errors.As(err, &refusal) || refusal.Kind != KindDisposable || refusal.Status() != http.StatusBadRequest {
			t.Errorf("%s: err = %v", address, err)
		}
	}
}

func TestEmailPolicy_RefusesADomainThatCannotReceiveMail(t *testing.T) {
	policy := NewEmailPolicy(newDNS())
	for _, address := range []string{"a@no-such-domain.test", "a@nullmx.test"} {
		err := policy.Check(context.Background(), address)
		var refusal *EmailRefusal
		if !errors.As(err, &refusal) || refusal.Kind != KindNoMail || refusal.Status() != http.StatusBadRequest {
			t.Errorf("%s: err = %v", address, err)
		}
	}
}

func TestEmailPolicy_FailsClosedWhenDNSFails(t *testing.T) {
	dns := newDNS()
	policy := NewEmailPolicy(dns)
	for _, address := range []string{"a@slow.test", "a@fail.test"} {
		err := policy.Check(context.Background(), address)
		var refusal *EmailRefusal
		if !errors.As(err, &refusal) || refusal.Kind != KindTryAgain || refusal.Status() != http.StatusServiceUnavailable {
			t.Errorf("%s: err = %v", address, err)
		}
	}
	dns.hostErr = map[string]error{"nomx-but-a.test": &net.DNSError{Err: "timeout", IsTimeout: true}}
	dns.mx["nomx-but-a.test"] = nil
	delete(dns.mx, "nomx-but-a.test")
	if err := policy.Check(context.Background(), "a@nomx-but-a.test"); statusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("a failed address lookup allowed the domain: %v", err)
	}
}

func TestEmailPolicy_LooksUpWithAShortDeadline(t *testing.T) {
	dns := newDNS()
	policy := NewEmailPolicy(dns)
	if err := policy.Check(context.Background(), "a@gmail.com"); err != nil {
		t.Fatal(err)
	}
	if len(dns.deadlines) != 1 || dns.deadlines[0] <= 5*time.Second || dns.deadlines[0] > 6*time.Second {
		t.Errorf("deadlines = %v, want one of about 6s", dns.deadlines)
	}
}

func TestEmailPolicy_RefusesMalformedAddresses(t *testing.T) {
	policy := NewEmailPolicy(newDNS())
	for _, address := range []string{"", "no-at-sign", "a@", "@gmail.com", "a@b@gmail.com"} {
		if err := policy.Check(context.Background(), address); !errors.Is(err, ErrEmailRefused) {
			t.Errorf("%q: err = %v", address, err)
		}
	}
}

func TestTempMailLists_HoldWhatTheTicketNames(t *testing.T) {
	for _, domain := range []string{"fldubai.com", "sharklasers.com", "guerrillamailblock.com", "tanpony.com", "macsnipe.com",
		"en2ch.com", "bankwen.com", "javafs.com", "yuexinmail.edu.kg", "nimbusmono.com", "tinkmail.me"} {
		if !DisposableDomain(domain) {
			t.Errorf("%s is not on the disposable lists", domain)
		}
	}
	for _, domain := range []string{"gmail.com", "outlook.com", "proton.me", "acme.example"} {
		if DisposableDomain(domain) {
			t.Errorf("%s is on the disposable lists", domain)
		}
	}
	for _, host := range []string{"in.mail.tm", "mail.guerrillamail.com", "MX.Tinkmail.me."} {
		if !TempMailHost(host) {
			t.Errorf("%s is not a temp-mail mail host", host)
		}
	}
	for _, host := range []string{"gmail-smtp-in.l.google.com", "route1.mx.cloudflare.net", "mx.acme.example"} {
		if TempMailHost(host) {
			t.Errorf("%s is a temp-mail mail host", host)
		}
	}
}

func TestEmailPolicy_CachesGoodAnswersForTenMinutes(t *testing.T) {
	dns := newDNS()
	policy := NewEmailPolicy(dns)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	policy.now = func() time.Time { return now }

	for range 3 {
		if err := policy.Check(context.Background(), "a@gmail.com"); err != nil {
			t.Fatal(err)
		}
	}
	if len(dns.asked) != 1 {
		t.Fatalf("lookups = %v, want one", dns.asked)
	}
	now = now.Add(9 * time.Minute)
	_ = policy.Check(context.Background(), "b@GMAIL.com")
	if len(dns.asked) != 1 {
		t.Errorf("a 9 minute old answer was asked again: %v", dns.asked)
	}
	now = now.Add(2 * time.Minute)
	_ = policy.Check(context.Background(), "a@gmail.com")
	if len(dns.asked) != 2 {
		t.Errorf("an 11 minute old answer was reused: %v", dns.asked)
	}
}

func TestEmailPolicy_NeverCachesARefusalOrAFailure(t *testing.T) {
	dns := newDNS()
	policy := NewEmailPolicy(dns)
	for _, address := range []string{"a@fresh-throw.test", "a@slow.test", "a@no-such-domain.test"} {
		_ = policy.Check(context.Background(), address)
		before := len(dns.asked)
		_ = policy.Check(context.Background(), address)
		if len(dns.asked) == before {
			t.Errorf("%s: the second answer came from the cache", address)
		}
	}
}

func TestEmailPolicy_ListedDomainsAreRefusedEvenIfCached(t *testing.T) {
	dns := newDNS()
	dns.mx["later-listed.test"] = []string{"mx.later-listed.test."}
	policy := NewEmailPolicy(dns)
	if err := policy.Check(context.Background(), "a@later-listed.test"); err != nil {
		t.Fatal(err)
	}
	if err := policy.Check(context.Background(), "a@fldubai.com"); !errors.Is(err, ErrEmailRefused) {
		t.Fatal("a listed domain passed")
	}
}
