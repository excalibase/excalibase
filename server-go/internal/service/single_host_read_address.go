package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A single host runs one copy of every database (ADR 0038), so it never has a
// replica to read from. The read address reports that rather than pointing at
// the primary, which would take writes.

// ReadAddress reports that the project has no read address on this host.
func (s *SingleHostEndpointService) ReadAddress(_ context.Context, projectID string) (DBReadAddressView, error) {
	inst, err := s.project(projectID)
	if err != nil {
		return DBReadAddressView{}, err
	}
	view := DBReadAddressView{ProjectID: inst.ProjectID, SingleHost: true, Reason: domain.ReadAddressSingleHost}
	if inst.DBType == domain.MySQL {
		view.Engine = domain.CredentialEngineMySQL
	}
	return view, nil
}

// SetReadAddress is refused: there is no replica on a single host.
func (s *SingleHostEndpointService) SetReadAddress(context.Context, string, bool) (DBReadAddressView, error) {
	return DBReadAddressView{}, fmt.Errorf("%w: a single host runs one copy of the database; a read address needs replicas on a Kubernetes cluster with a node per copy", ErrDBEndpointUnsupported)
}
