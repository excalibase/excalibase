package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-757: a project whose owner is suspended must not read as plain "Active".
func TestProjectList_MarksProjectsWhoseOwnerIsSuspended(t *testing.T) {
	h := &ProvisioningHandler{}
	h.SetSuspendedOrgs(func(context.Context) (map[string]bool, error) { return map[string]bool{"o-bad": true}, nil })

	insts := []*domain.DatabaseInstance{
		{ProjectID: "p1", OrgID: "o-bad", Status: "ACTIVE"},
		{ProjectID: "p2", OrgID: "o-good", Status: "ACTIVE"},
	}
	raw, err := json.Marshal(h.projectListResponse(context.Background(), insts))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out[0]["ownerSuspended"] != true {
		t.Fatalf("p1 = %v", out[0])
	}
	if _, present := out[1]["ownerSuspended"]; present {
		t.Fatalf("p2 must not carry the marker: %v", out[1])
	}
}

func TestProjectResponse_ALookupFailureLeavesTheProjectUnmarked(t *testing.T) {
	h := &ProvisioningHandler{}
	h.SetSuspendedOrgs(func(context.Context) (map[string]bool, error) { return nil, errors.New("db down") })
	view := h.projectResponse(context.Background(), &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o-bad"})
	if view.OwnerSuspended {
		t.Fatal("a failed lookup must not claim a suspension")
	}
}
