package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func TestSweep_ServesEveryIssuedAppHostCertificate(t *testing.T) {
	f := newDomainFixture(t)
	f.svc.Sweep(context.Background())
	if !slices.Contains(f.kube.Calls, "AttachIssuedAppHostCertificates") {
		t.Fatalf("calls = %v: app hostnames must be followed even with no custom domain", f.kube.Calls)
	}
}

func TestSweep_AFailedAttachDoesNotStopTheDomainSweep(t *testing.T) {
	f := newDomainFixture(t)
	d := verified(t, f, "shop.example.com")
	f.kube.HostCertAttachErr = errors.New("api server down")
	f.kube.DomainCerts["shop.example.com"] = k8s.CertificateState{Ready: true}
	f.svc.Sweep(context.Background())
	if got := f.store.status(d.ID); got.Status != apphost.DomainActive {
		t.Fatalf("domain = %+v", got)
	}
}

func TestHostCertificate_ReportsTheAppHostnamesCertificate(t *testing.T) {
	f := newDomainFixture(t)
	ctx := context.Background()
	key := testDeployNamespace + "/" + f.app.Name
	cases := []struct {
		name   string
		state  *k8s.CertificateState
		err    error
		status string
		reason string
	}{
		{"not deployed yet", nil, k8s.ErrNoCertificate, HostCertificateNone, ""},
		{"issuing", &k8s.CertificateState{}, nil, apphost.DomainIssuing, ""},
		{"failed", &k8s.CertificateState{Failure: "acme: rate limited"}, nil, apphost.DomainIssueFailed, "acme: rate limited"},
		{"issued", &k8s.CertificateState{Ready: true}, nil, apphost.DomainActive, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			delete(f.kube.HostCerts, key)
			if tc.state != nil {
				f.kube.HostCerts[key] = *tc.state
			}
			view, err := f.svc.HostCertificate(ctx, f.app.ProjectID, f.app.ID)
			if err != nil {
				t.Fatal(err)
			}
			if view.Hostname != appHost || view.Status != tc.status || view.FailureReason != tc.reason {
				t.Fatalf("view = %+v", view)
			}
		})
	}
}

func TestHostCertificate_AnInternalServiceHasNone(t *testing.T) {
	f := newDomainFixture(t)
	f.app.Internal = true
	view, err := f.svc.HostCertificate(context.Background(), f.app.ProjectID, f.app.ID)
	if err != nil || view.Status != HostCertificateNone || view.Hostname != "" {
		t.Fatalf("view = %+v, %v", view, err)
	}
}

func TestHostCertificate_ARealFailureIsAnError(t *testing.T) {
	f := newDomainFixture(t)
	f.kube.HostCertErr = errors.New("forbidden")
	if _, err := f.svc.HostCertificate(context.Background(), f.app.ProjectID, f.app.ID); err == nil {
		t.Fatal("a failed read is not 'no certificate'")
	}
	if _, err := f.svc.HostCertificate(context.Background(), f.app.ProjectID, "nope"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("missing app: %v", err)
	}
}
