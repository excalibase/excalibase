package natsauth

import (
	"context"
	"errors"
	"testing"
)

type seedStore struct {
	hashes   map[string]string
	projects map[string]string
	err      error
}

func newSeedStore() *seedStore {
	return &seedStore{hashes: map[string]string{}, projects: map[string]string{}}
}

func (s *seedStore) UpsertNatsCredential(_ context.Context, principal, projectID, hash string) error {
	if s.err != nil {
		return s.err
	}
	s.hashes[principal] = hash
	s.projects[principal] = projectID
	return nil
}

func (s *seedStore) LookupNatsCredentialHash(_ context.Context, principal string) (string, bool, error) {
	hash, ok := s.hashes[principal]
	return hash, ok, nil
}

func TestSeedServicePrincipalsStoresHashesOnly(t *testing.T) {
	store := newSeedStore()
	passwords := map[string]string{
		PrincipalProvisioning: "prov-pw",
		PrincipalGraphQL:      "gql-pw",
		PrincipalPgDog:        "pgdog-pw",
	}

	if err := SeedServicePrincipals(context.Background(), store, passwords); err != nil {
		t.Fatalf("SeedServicePrincipals: %v", err)
	}

	for principal, password := range passwords {
		if store.hashes[principal] == password {
			t.Errorf("%s: plaintext password persisted", principal)
		}
		if store.projects[principal] != "" {
			t.Errorf("%s: service principal scoped to project %q", principal, store.projects[principal])
		}
		if err := Authenticate(context.Background(), store, principal, password); err != nil {
			t.Errorf("%s: seeded credential does not authenticate: %v", principal, err)
		}
	}
}

func TestSeedServicePrincipalsSkipsBlankPasswords(t *testing.T) {
	store := newSeedStore()

	err := SeedServicePrincipals(context.Background(), store, map[string]string{
		PrincipalProvisioning: "prov-pw",
		PrincipalGraphQL:      "",
	})
	if err != nil {
		t.Fatalf("SeedServicePrincipals: %v", err)
	}
	if _, ok := store.hashes[PrincipalGraphQL]; ok {
		t.Error("blank password was seeded")
	}
	if _, ok := store.hashes[PrincipalProvisioning]; !ok {
		t.Error("configured password was not seeded")
	}
}

func TestSeedServicePrincipalsIgnoresUnknownNames(t *testing.T) {
	store := newSeedStore()

	if err := SeedServicePrincipals(context.Background(), store, map[string]string{"svc-attacker": "pw"}); err != nil {
		t.Fatalf("SeedServicePrincipals: %v", err)
	}
	if len(store.hashes) != 0 {
		t.Errorf("seeded %v, want nothing", store.hashes)
	}
}

func TestSeedServicePrincipalsFailsLoudly(t *testing.T) {
	store := newSeedStore()
	store.err = errors.New("db down")

	if err := SeedServicePrincipals(context.Background(), store, map[string]string{PrincipalGraphQL: "pw"}); err == nil {
		t.Error("store failure was swallowed")
	}
	if err := SeedServicePrincipals(context.Background(), nil, nil); err == nil {
		t.Error("nil store accepted")
	}
}
