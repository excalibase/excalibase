package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// A project's database allow-list (EXC-665) is rendered onto each of its public
// Services as loadBalancerSourceRanges, which the datapath enforces at the node
// before any SNAT. Like every other endpoint write, a change is recorded only
// after the Services are observed carrying it.

// ErrDBAllowListOff is a write while the dballowlist flag is off.
var ErrDBAllowListOff = errors.New("database allow-lists are not enabled on this platform")

// ErrDBAllowListNotChosen refuses to open a port before its Admin has said who may connect.
var ErrDBAllowListNotChosen = errors.New("choose who may connect first: list addresses or allow any address, then open the public port")

// ErrDBAllowListNotObserved is a Service read back without the list asked for.
var ErrDBAllowListNotObserved = errors.New("the allow-list was not confirmed on the database endpoint; retry to continue")

// DBAllowListView is a project's allow-list as a caller sees it.
type DBAllowListView struct {
	ProjectID     string
	Access        domain.DBSourceAccess
	Sources       []string
	Max           int
	PublicEnabled bool
	SingleHost    bool
	// PreviousAccess and PreviousSources are what a change replaced, read
	// under the lease, for the audit.
	PreviousAccess  domain.DBSourceAccess
	PreviousSources []string
}

// sourcePolicy is how the Services are rendered. enforced is the flag: with
// it off nothing about a Service changes.
type sourcePolicy struct {
	enforced    bool
	ranges      []string
	noNodePorts bool
}

// UseSourceAllowList wires the allow-list after construction.
func (s *DBEndpointService) UseSourceAllowList(lists storage.DBSourceAllowListStore, flags features.Flags) {
	s.allowList, s.flags = lists, flags
}

func (s *DBEndpointService) allowListOn(ctx context.Context) bool {
	return s.allowList != nil && features.Enabled(ctx, s.flags, features.DBAllowList)
}

// servicePolicy reads the stored choice. A stored list is rendered even with
// the flag off, so a rollback never opens a restricted port; with the flag
// off and no list, Services render exactly as before the flag.
func (s *DBEndpointService) servicePolicy(ctx context.Context, projectID string) (sourcePolicy, error) {
	if s.allowList == nil {
		return sourcePolicy{}, nil
	}
	list, err := s.allowList.GetDBSourceAllowList(ctx, projectID)
	if err != nil {
		return sourcePolicy{}, err
	}
	if !s.allowListOn(ctx) && list.Access != domain.DBSourceAccessListed {
		return sourcePolicy{}, nil
	}
	return policyFor(list), nil
}

func policyFor(list domain.DBSourceAllowList) sourcePolicy {
	return sourcePolicy{enforced: true, ranges: list.SourceRanges(), noNodePorts: true}
}

// requireChosenSources refuses a new public port the Admin has not scoped.
func (s *DBEndpointService) requireChosenSources(ctx context.Context, projectID string) error {
	if !s.allowListOn(ctx) {
		return nil
	}
	list, err := s.allowList.GetDBSourceAllowList(ctx, projectID)
	if err != nil {
		return err
	}
	if !list.Chosen() {
		return ErrDBAllowListNotChosen
	}
	return nil
}

func (s *DBEndpointService) forgetSourceAllowList(ctx context.Context, projectID string) error {
	if s.allowList == nil {
		return nil
	}
	return s.allowList.DeleteDBSourceAllowList(ctx, projectID)
}

// confirmSourceRanges reads one Service back and compares its ranges.
func (s *DBEndpointService) confirmSourceRanges(ctx context.Context, namespace string, spec k8s.PublicDBServiceSpec) error {
	observed, err := s.kube.PublicDBServiceSourceRanges(ctx, namespace, spec.Name)
	if err != nil {
		return fmt.Errorf("confirm database allow-list: %w", err)
	}
	if !sameRanges(observed, spec.SourceRanges) {
		return ErrDBAllowListNotObserved
	}
	return nil
}

func sameRanges(observed, want []string) bool {
	left, right := slices.Clone(observed), slices.Clone(want)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// serializeWithAllowList takes the project lease while the flag is on, so an
// open cannot render a list read before a concurrent change records.
func (s *DBEndpointService) serializeWithAllowList(ctx context.Context, projectID string) (func(), error) {
	if !s.allowListOn(ctx) {
		return func() {
			// Nothing was claimed, so there is nothing to release.
		}, nil
	}
	release, claimed, err := s.claimer.Claim(ctx, projectID, OperationAllowList)
	if err != nil {
		return nil, fmt.Errorf("claim project for %s: %w", OperationAllowList, err)
	}
	if !claimed {
		return nil, fmt.Errorf("%w (%s)", ErrProjectOperationRunning, projectID)
	}
	return release, nil
}

// AllowedSources reports the project's allow-list.
func (s *DBEndpointService) AllowedSources(ctx context.Context, projectID string) (DBAllowListView, error) {
	if !s.allowListOn(ctx) {
		return DBAllowListView{}, ErrDBAllowListOff
	}
	if _, err := s.project(projectID); err != nil {
		return DBAllowListView{}, err
	}
	list, err := s.allowList.GetDBSourceAllowList(ctx, projectID)
	if err != nil {
		return DBAllowListView{}, err
	}
	return s.allowListView(ctx, list)
}

// SetAllowedSources applies a parsed list. A servable public project has its
// Services re-rendered and read back first; any other project records it for
// its next Publish.
func (s *DBEndpointService) SetAllowedSources(ctx context.Context, projectID string, list domain.DBSourceAllowList) (DBAllowListView, error) {
	if !s.allowListOn(ctx) {
		return DBAllowListView{}, ErrDBAllowListOff
	}
	if !list.Chosen() {
		return DBAllowListView{}, fmt.Errorf("%w: access %q is not a choice", domain.ErrInvalidDBSourceAllowList, list.Access)
	}
	release, err := s.serializeWithAllowList(ctx, projectID)
	if err != nil {
		return DBAllowListView{}, err
	}
	defer release()

	inst, err := s.project(projectID)
	if err != nil {
		return DBAllowListView{}, err
	}
	previous, err := s.allowList.GetDBSourceAllowList(ctx, projectID)
	if err != nil {
		return DBAllowListView{}, err
	}
	list.ProjectID = projectID
	if err := s.applyToServices(ctx, inst, policyFor(list)); err != nil {
		return DBAllowListView{}, s.restoreServices(ctx, inst, err)
	}
	saved, err := s.allowList.SetDBSourceAllowList(ctx, list)
	if err != nil {
		return DBAllowListView{}, s.restoreServices(ctx, inst, err)
	}
	view, err := s.allowListView(ctx, saved)
	if err != nil {
		return DBAllowListView{}, err
	}
	view.PreviousAccess, view.PreviousSources = previous.Access, slices.Clone(previous.Sources)
	return view, nil
}

// restoreServices puts the recorded choice back on the Services after a change
// failed part way, so none is left more open than what is recorded.
func (s *DBEndpointService) restoreServices(ctx context.Context, inst *domain.DatabaseInstance, cause error) error {
	policy, err := s.servicePolicy(ctx, inst.ProjectID)
	if err == nil {
		err = s.applyToServices(ctx, inst, policy)
	}
	if err != nil {
		log.Printf("ERROR: restore the allow-list of %s after a failed change: %v", inst.ProjectID, err)
	}
	return cause
}

func (s *DBEndpointService) applyToServices(ctx context.Context, inst *domain.DatabaseInstance, policy sourcePolicy) error {
	if !s.publicOffered() || !servableNow(inst.Status) {
		return nil
	}
	endpoint, err := s.endpoints.GetDatabaseEndpoint(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	if !endpoint.IsPublic() {
		return nil
	}
	mongoPort, err := s.mongoPort(ctx, inst)
	if err != nil {
		return err
	}
	return s.publishServicesWith(ctx, inst, endpoint.Port, mongoPort, policy)
}

func (s *DBEndpointService) allowListView(ctx context.Context, list domain.DBSourceAllowList) (DBAllowListView, error) {
	endpoint, err := s.endpoints.GetDatabaseEndpoint(ctx, list.ProjectID)
	if err != nil {
		return DBAllowListView{}, err
	}
	return DBAllowListView{
		ProjectID:     list.ProjectID,
		Access:        list.Access,
		Sources:       slices.Clone(list.Sources),
		Max:           domain.MaxDBAllowedSources,
		PublicEnabled: endpoint.PublicEnabled,
	}, nil
}
