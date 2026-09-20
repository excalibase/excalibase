package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-365: the engine fetches the exposure list in the same round as the RLS
// and column policies. Until the capability table named table-grants the
// control plane answered 403, the engine cached nothing, and every tenant
// GraphQL query returned empty. These tests drive the REAL router built by
// buildRouter with the engine's real chart permission list.

const grantsPath = "/api/provision/" + contractProject + "/table-grants/"

func TestEngineTokenReadsTableGrants(t *testing.T) {
	router, who, grants := contractRouter(t)
	seeded := domain.TableGrant{
		ID: "g-1", ProjectID: contractProject, Resource: "public.customer",
		Operations: []domain.Operation{domain.OpSelect},
		Role:       domain.GrantRoleAuthenticated, Enabled: true,
	}
	if err := grants.UpsertGrant(context.Background(), &seeded); err != nil {
		t.Fatalf("UpsertGrant: %v", err)
	}

	w := contractRequest(router, serviceCall{caller: svcGraphql, method: http.MethodGet, path: grantsPath}, who[svcGraphql])

	if w.Code != http.StatusOK {
		t.Fatalf("engine reading table grants = %d, want 200: %s", w.Code, w.Body.String())
	}
	var set domain.TableGrantSet
	if err := json.Unmarshal(w.Body.Bytes(), &set); err != nil {
		t.Fatalf("decode grant set: %v (body %s)", err, w.Body.String())
	}
	if !set.Enforced {
		t.Fatal("enforced flag lost on the wire; the engine cannot tell deny-all from unfiltered")
	}
	if len(set.Grants) != 1 || set.Grants[0].Resource != seeded.Resource {
		t.Fatalf("grants = %+v, want the seeded %s", set.Grants, seeded.Resource)
	}
}

// A service principal holding a different capability must not read another
// project surface it was never granted.
func TestAuthTokenIsRefusedTableGrants(t *testing.T) {
	router, who, _ := contractRouter(t)

	w := contractRequest(router, serviceCall{caller: svcAuth, method: http.MethodGet, path: grantsPath}, who[svcAuth])

	if w.Code != http.StatusForbidden {
		t.Fatalf("auth service reading table grants = %d, want 403: %s", w.Code, w.Body.String())
	}
}

// policies:read is a READ grant: the engine may cache the exposure list and
// may not edit it, or a compromised engine could grant itself every table.
func TestEngineTokenCannotWriteTableGrants(t *testing.T) {
	router, who, _ := contractRouter(t)
	writes := []struct{ method, path string }{
		{http.MethodPost, grantsPath},
		{http.MethodPatch, grantsPath + "g-1"},
		{http.MethodDelete, grantsPath + "g-1"},
	}
	for _, write := range writes {
		t.Run(write.method, func(t *testing.T) {
			call := serviceCall{caller: svcGraphql, method: write.method, path: write.path}
			if w := contractRequest(router, call, who[svcGraphql]); w.Code != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403: %s", write.method, write.path, w.Code, w.Body.String())
			}
		})
	}
}
