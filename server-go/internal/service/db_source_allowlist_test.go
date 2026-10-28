package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// fakeAllowListStore keeps each project's choice in memory.
type fakeAllowListStore struct {
	mu      sync.Mutex
	lists   map[string]domain.DBSourceAllowList
	deletes []string
}

func newFakeAllowListStore() *fakeAllowListStore {
	return &fakeAllowListStore{lists: map[string]domain.DBSourceAllowList{}}
}

func (f *fakeAllowListStore) GetDBSourceAllowList(_ context.Context, projectID string) (domain.DBSourceAllowList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if list, ok := f.lists[projectID]; ok {
		return list, nil
	}
	return domain.DBSourceAllowList{ProjectID: projectID}, nil
}

func (f *fakeAllowListStore) SetDBSourceAllowList(_ context.Context, list domain.DBSourceAllowList) (domain.DBSourceAllowList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists[list.ProjectID] = list
	return list, nil
}

func (f *fakeAllowListStore) DeleteDBSourceAllowList(_ context.Context, projectID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.lists, projectID)
	f.deletes = append(f.deletes, projectID)
	return nil
}

var _ storage.DBSourceAllowListStore = (*fakeAllowListStore)(nil)

func allowListFlags(on bool) features.Flags {
	if on {
		return features.NewStatic(features.DBAllowList)
	}
	return features.NewStatic()
}

func mustAllowList(t *testing.T, allowAny bool, sources ...string) domain.DBSourceAllowList {
	t.Helper()
	list, err := domain.ParseDBSourceAllowList(allowAny, sources)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return list
}

func (h *endpointHarness) withAllowList(on bool) *fakeAllowListStore {
	lists := newFakeAllowListStore()
	h.svc.UseSourceAllowList(lists, allowListFlags(on))
	return lists
}

func (h *endpointHarness) publicSpecRanges(t *testing.T) ([]string, bool) {
	t.Helper()
	spec, ok := h.kube.PublicDBServices[endpointNamespace+"/"+domain.DBEndpointServiceName(endpointProject)]
	if !ok {
		t.Fatal("no public Service")
	}
	return spec.SourceRanges, spec.NoNodePorts
}

func TestAllowListOffRendersServicesAsBefore(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.withAllowList(false)
	if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	if ranges, noNodePorts := h.publicSpecRanges(t); ranges != nil || noNodePorts {
		t.Fatalf("ranges %v, noNodePorts %v: want the pre-flag Service", ranges, noNodePorts)
	}
	if _, err := h.svc.SetAllowedSources(context.Background(), endpointProject, mustAllowList(t, true)); !errors.Is(err, ErrDBAllowListOff) {
		t.Fatalf("want ErrDBAllowListOff, got %v", err)
	}
}

func TestOpeningThePortNeedsAChoiceFirst(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.withAllowList(true)
	if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); !errors.Is(err, ErrDBAllowListNotChosen) {
		t.Fatalf("want ErrDBAllowListNotChosen, got %v", err)
	}
	if h.serviceExists(t) {
		t.Fatal("no Service may exist before the Admin chooses who may connect")
	}
	if endpoint, _ := h.store.GetDatabaseEndpoint(context.Background(), endpointProject); endpoint.Port != 0 {
		t.Fatalf("no port may be taken for a refused open, got %d", endpoint.Port)
	}
}

func TestAListChosenBeforeOpeningIsOnTheService(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	lists := h.withAllowList(true)
	ctx := context.Background()
	view, err := h.svc.SetAllowedSources(ctx, endpointProject, mustAllowList(t, false, "198.51.100.7"))
	if err != nil {
		t.Fatalf("SetAllowedSources: %v", err)
	}
	if view.Access != domain.DBSourceAccessListed || view.PublicEnabled || view.Max != domain.MaxDBAllowedSources {
		t.Fatalf("view %+v", view)
	}
	if h.serviceExists(t) {
		t.Fatal("choosing a list must not open the port")
	}
	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	ranges, noNodePorts := h.publicSpecRanges(t)
	if !slices.Equal(ranges, []string{"198.51.100.7/32"}) || !noNodePorts {
		t.Fatalf("ranges %v, noNodePorts %v", ranges, noNodePorts)
	}
	if got, _ := lists.GetDBSourceAllowList(ctx, endpointProject); got.Access != domain.DBSourceAccessListed {
		t.Fatalf("stored %+v", got)
	}
}

func TestChangingTheListOfAnOpenPortReachesTheServiceBeforeItIsRecorded(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	lists := h.withAllowList(true)
	ctx := context.Background()
	if _, err := h.svc.SetAllowedSources(ctx, endpointProject, mustAllowList(t, true)); err != nil {
		t.Fatalf("allow any: %v", err)
	}
	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	if ranges, noNodePorts := h.publicSpecRanges(t); ranges != nil || !noNodePorts {
		t.Fatalf("allow any: ranges %v, noNodePorts %v", ranges, noNodePorts)
	}
	view, err := h.svc.SetAllowedSources(ctx, endpointProject, mustAllowList(t, false, "203.0.113.0/24"))
	if err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if ranges, _ := h.publicSpecRanges(t); !slices.Equal(ranges, []string{"203.0.113.0/24"}) {
		t.Fatalf("Service ranges %v", ranges)
	}
	if !view.PublicEnabled || !slices.Equal(view.Sources, []string{"203.0.113.0/24"}) {
		t.Fatalf("view %+v", view)
	}

	h.kube.SourceRangesReadBack = []string{"192.0.2.0/24"}
	_, err = h.svc.SetAllowedSources(ctx, endpointProject, mustAllowList(t, false, "198.51.100.0/24"))
	if !errors.Is(err, ErrDBAllowListNotObserved) {
		t.Fatalf("want ErrDBAllowListNotObserved, got %v", err)
	}
	if got, _ := lists.GetDBSourceAllowList(ctx, endpointProject); !slices.Equal(got.Sources, []string{"203.0.113.0/24"}) {
		t.Fatalf("an unconfirmed list must not be recorded, stored %+v", got)
	}
}

func TestAPausedProjectRecordsTheListAndItsResumeRendersIt(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.withAllowList(true)
	ctx := context.Background()
	if _, err := h.svc.SetAllowedSources(ctx, endpointProject, mustAllowList(t, true)); err != nil {
		t.Fatalf("allow any: %v", err)
	}
	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	h.instance.Status = "PAUSED"
	if err := h.svc.Withdraw(ctx, h.instance); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	h.kube.Calls = nil
	if _, err := h.svc.SetAllowedSources(ctx, endpointProject, mustAllowList(t, false, "198.51.100.7")); err != nil {
		t.Fatalf("SetAllowedSources while paused: %v", err)
	}
	for _, call := range h.kube.Calls {
		if strings.HasPrefix(call, "EnsurePublicDBService") {
			t.Fatalf("a paused project has no Service to change, got %s", call)
		}
	}
	h.instance.Status = "ACTIVE"
	if err := h.svc.Publish(ctx, h.instance); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if ranges, _ := h.publicSpecRanges(t); !slices.Equal(ranges, []string{"198.51.100.7/32"}) {
		t.Fatalf("resumed with %v", ranges)
	}
}

func TestAPortOpenedBeforeTheFlagStaysOpenToAnyAddress(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.withAllowList(false)
	ctx := context.Background()
	if _, err := h.svc.SetPublic(ctx, endpointProject, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	h.withAllowList(true)
	if err := h.svc.Publish(ctx, h.instance); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if ranges, noNodePorts := h.publicSpecRanges(t); ranges != nil || !noNodePorts {
		t.Fatalf("ranges %v, noNodePorts %v", ranges, noNodePorts)
	}
	view, err := h.svc.AllowedSources(ctx, endpointProject)
	if err != nil || view.Access != domain.DBSourceAccessUnset || !view.PublicEnabled {
		t.Fatalf("view %+v, %v", view, err)
	}
}

func TestReleaseForgetsTheAllowList(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	lists := h.withAllowList(true)
	ctx := context.Background()
	if _, err := h.svc.SetAllowedSources(ctx, endpointProject, mustAllowList(t, true)); err != nil {
		t.Fatalf("allow any: %v", err)
	}
	if err := h.svc.Release(ctx, h.instance); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if !slices.Contains(lists.deletes, endpointProject) {
		t.Fatal("the allow-list must go with the project")
	}
}

func TestChangingTheListWaitsForTheProjectLease(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.withAllowList(true)
	claimer := newInProcessOperationClaimer()
	h.svc.claimer = claimer
	release, claimed, err := claimer.Claim(context.Background(), endpointProject, OperationPause)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()
	if _, err := h.svc.SetAllowedSources(context.Background(), endpointProject, mustAllowList(t, true)); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("want ErrProjectOperationRunning, got %v", err)
	}
}

func TestEveryPublicServiceOfADocumentDBProjectCarriesTheList(t *testing.T) {
	f := newMongoEndpointFixture(t, true)
	f.svc.UseSourceAllowList(newFakeAllowListStore(), allowListFlags(true))
	ctx := context.Background()
	if _, err := f.svc.SetAllowedSources(ctx, f.inst.ProjectID, mustAllowList(t, false, "2001:db8::/32")); err != nil {
		t.Fatalf("SetAllowedSources: %v", err)
	}
	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	for _, name := range []string{domain.DBEndpointServiceName(f.inst.ProjectID), domain.DBEndpointMongoServiceName(f.inst.ProjectID)} {
		spec, ok := f.kube.PublicDBServices[f.inst.Namespace+"/"+name]
		if !ok || !slices.Equal(spec.SourceRanges, []string{"2001:db8::/32"}) || !spec.NoNodePorts {
			t.Errorf("%s: %+v", name, spec)
		}
	}
}

func TestAMySQLProjectsServiceCarriesTheList(t *testing.T) {
	f := newMySQLEndpointFixture(t)
	f.svc.UseSourceAllowList(newFakeAllowListStore(), allowListFlags(true))
	ctx := context.Background()
	if _, err := f.svc.SetAllowedSources(ctx, f.inst.ProjectID, mustAllowList(t, false, "198.51.100.0/24")); err != nil {
		t.Fatalf("SetAllowedSources: %v", err)
	}
	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	spec := f.kube.PublicDBServices[f.inst.Namespace+"/"+f.inst.ProjectID+"-mysql-public"]
	if !slices.Equal(spec.SourceRanges, []string{"198.51.100.0/24"}) || !spec.NoNodePorts {
		t.Fatalf("spec %+v", spec)
	}
}

// An open racing a list change could render the list it read before the change.
func TestOpeningThePortWaitsForTheProjectLeaseWhileAllowListsAreOn(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.withAllowList(true)
	claimer := newInProcessOperationClaimer()
	h.svc.claimer = claimer
	if _, err := h.svc.SetAllowedSources(context.Background(), endpointProject, mustAllowList(t, true)); err != nil {
		t.Fatalf("allow any: %v", err)
	}
	release, claimed, err := claimer.Claim(context.Background(), endpointProject, OperationAllowList)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()
	if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("want ErrProjectOperationRunning, got %v", err)
	}
}

func TestOpeningThePortTakesNoLeaseWhileAllowListsAreOff(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.withAllowList(false)
	claimer := newInProcessOperationClaimer()
	h.svc.claimer = claimer
	release, _, _ := claimer.Claim(context.Background(), endpointProject, OperationPause)
	defer release()
	if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); err != nil {
		t.Fatalf("SetPublic with the flag off behaves as before: %v", err)
	}
}
