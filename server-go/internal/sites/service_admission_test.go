package sites_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

// heldBody signals when the upload starts being read and holds it until let go.
type heldBody struct {
	started chan struct{}
	release chan struct{}
	body    io.Reader
	once    bool
}

func newHeldBody(content []byte) *heldBody {
	return &heldBody{started: make(chan struct{}), release: make(chan struct{}), body: bytes.NewReader(content)}
}

func (h *heldBody) Read(buffer []byte) (int, error) {
	if !h.once {
		h.once = true
		close(h.started)
		<-h.release
	}
	return h.body.Read(buffer)
}

func TestUploadsInFlightAreBoundedPerProjectAndOverall(t *testing.T) {
	store, publisher, origins := sitestest.NewMemoryStore(), sitestest.NewPublisher(), sitestest.NewOrigins()
	service, err := sites.NewService(store, publisher, origins, sites.Options{BaseDomain: testDomain, SpoolDir: t.TempDir(), MaxDeploysInFlight: 1})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	shop, _ := service.Create(ctx, testProject, "shop", nil, testActor)
	other, _ := service.Create(ctx, "proj-other", "other-shop", nil, testActor)
	held := newHeldBody(viteDist(t))
	done := make(chan error, 1)
	go func() {
		_, err := service.Deploy(ctx, sites.DeployRequest{ProjectID: testProject, SiteID: shop.ID, Body: held})
		done <- err
	}()
	<-held.started

	_, err = service.Deploy(ctx, sites.DeployRequest{ProjectID: testProject, SiteID: shop.ID, Body: bytes.NewReader(viteDist(t))})
	if !errors.Is(err, sites.ErrProjectDeployRunning) {
		t.Errorf("same project: err = %v, want one upload per project", err)
	}
	_, err = service.Deploy(ctx, sites.DeployRequest{ProjectID: "proj-other", SiteID: other.ID, Body: bytes.NewReader(viteDist(t))})
	if !errors.Is(err, sites.ErrTooManyDeploys) {
		t.Errorf("other project: err = %v, want the overall cap", err)
	}

	close(held.release)
	if err := <-done; err != nil {
		t.Fatalf("held deploy: %v", err)
	}
	if _, err := service.Deploy(ctx, sites.DeployRequest{ProjectID: "proj-other", SiteID: other.ID, Body: bytes.NewReader(viteDist(t))}); err != nil {
		t.Fatalf("after the first finished: %v", err)
	}
}
