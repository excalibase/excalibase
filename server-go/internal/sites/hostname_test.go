package sites

import (
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

func TestNormalizeHostnameTakesWhatPeoplePaste(t *testing.T) {
	for raw, want := range map[string]string{
		"www.example.com":              "www.example.com",
		"  WWW.Example.COM. ":          "www.example.com",
		"https://shop.example.co.uk/x": "shop.example.co.uk",
		"bücher.example.com":           "xn--bcher-kva.example.com",
	} {
		got, err := NormalizeHostname(raw, "excalibase.app", nil)
		if err != nil || got != want {
			t.Errorf("NormalizeHostname(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}

func TestNormalizeHostnameRefusesWhatCannotBeACustomDomain(t *testing.T) {
	for _, raw := range []string{
		"", "1.2.3.4", "[::1]", "::1", "example.com", "co.uk", "*.example.com", "user@www.example.com", "www.example.com:8443",
		"shop.excalibase.app", "excalibase.app", "www.excalibase.io", "excalibase.io", "a.b.excalibase.com", "app.excalibase.co.uk",
		"studio.platform.test", "ftp://www.example.com", "shop.example.xn--p1ai", "shop.example.рф",
	} {
		if got, err := NormalizeHostname(raw, "excalibase.app", []string{"platform.test"}); err == nil {
			t.Errorf("NormalizeHostname(%q) = %q, want a refusal", raw, got)
		}
	}
}

func TestHostnameStatusNeedsTheHostnameAndItsCertificateActive(t *testing.T) {
	cases := []struct {
		hostname, certificate, want string
	}{
		{"active", "active", HostnameActive},
		{"active_redeploying", "active", HostnameActive},
		{"active", "pending_validation", HostnamePending},
		{"pending", "active", HostnamePending},
		{"pending", "initializing", HostnamePending},
		{"blocked", "active", HostnameFailed},
		{"moved", "pending_validation", HostnameFailed},
		{"pending", "validation_timed_out", HostnameFailed},
		{"active", "expired", HostnameFailed},
	}
	for _, c := range cases {
		got := hostnameStatus(cfworkers.CustomHostname{Status: c.hostname, SSL: cfworkers.HostnameSSL{Status: c.certificate}})
		if got != c.want {
			t.Errorf("status(%s, %s) = %s, want %s", c.hostname, c.certificate, got, c.want)
		}
	}
}

func TestDNSRecordsNameTheCNAMETheOwnersProofAndTXTValidationTokens(t *testing.T) {
	answer := cfworkers.CustomHostname{
		Hostname: "www.example.com", Status: "pending",
		OwnershipVerification: cfworkers.OwnershipVerification{Type: "txt", Name: "_cf-custom-hostname.www.example.com", Value: "own-1"},
		SSL: cfworkers.HostnameSSL{Status: "pending_validation", ValidationRecords: []cfworkers.ValidationRecord{
			{TXTName: "_acme-challenge.www.example.com", TXTValue: "dcv-1"},
			{HTTPURL: "http://www.example.com/.well-known/pki-validation/x.txt", HTTPBody: "ignored"},
		}},
	}
	host := &CustomHostname{Hostname: "www.example.com", Validation: cfworkers.ValidationHTTP, VerificationToken: "tok"}
	httpRecords := dnsInstructions(host, "cname.excalibase.app", answer)
	want := []DNSInstruction{
		{Type: "CNAME", Name: "www.example.com", Value: "cname.excalibase.app", Purpose: PurposeRouting},
		{Type: "TXT", Name: "_excalibase-challenge.www.example.com", Value: "excalibase-verification=tok", Purpose: PurposeVerification},
	}
	if len(httpRecords) != 2 || httpRecords[0] != want[0] || httpRecords[1] != want[1] {
		t.Fatalf("http records = %+v", httpRecords)
	}
	host.Validation = cfworkers.ValidationTXT
	txtRecords := dnsInstructions(host, "cname.excalibase.app", answer)
	if len(txtRecords) != 4 {
		t.Fatalf("txt records = %+v", txtRecords)
	}
	if txtRecords[2] != (DNSInstruction{Type: "TXT", Name: "_cf-custom-hostname.www.example.com", Value: "own-1", Purpose: PurposeOwnership}) {
		t.Errorf("ownership = %+v", txtRecords[2])
	}
	if txtRecords[3] != (DNSInstruction{Type: "TXT", Name: "_acme-challenge.www.example.com", Value: "dcv-1", Purpose: PurposeCertificate}) {
		t.Errorf("certificate = %+v", txtRecords[3])
	}
	verified := time.Now()
	host.VerifiedAt = &verified
	answer.Status, answer.SSL.Status = "active", "active"
	if done := dnsInstructions(host, "cname.excalibase.app", answer); len(done) != 1 {
		t.Errorf("an active, proven hostname still asks for records: %+v", done)
	}
}

func TestHostnameErrorsCollectBothValidations(t *testing.T) {
	got := hostnameErrors(cfworkers.CustomHostname{
		VerificationErrors: []string{"custom hostname does not CNAME to this zone."},
		SSL:                cfworkers.HostnameSSL{ValidationErrors: []cfworkers.ValidationError{{Message: "CAA record prevents issuance"}, {Message: ""}}},
	})
	if strings.Join(got, "|") != "custom hostname does not CNAME to this zone.|CAA record prevents issuance" {
		t.Fatalf("errors = %v", got)
	}
}
