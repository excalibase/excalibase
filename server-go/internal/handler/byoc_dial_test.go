package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/byoc"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// The stored BYOC target points at loopback (as if the DNS name was rebound
// after registration). Every platform dial site must refuse it through the
// guard, while a managed project on the same host is dialled as before.
func byocDialFixture(mode domain.DeploymentMode) (*inMemoryInstanceStore, *fakeVault) {
	insts := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		"proj-dial": {ProjectID: "proj-dial", OrgID: "default", DeploymentMode: mode},
	}}
	v := newFakeVault()
	v.Put("projects/proj-dial/credentials/excalibase_app", map[string]string{
		"host": "127.0.0.1", "port": "1", "username": "u", "password": "p", "database": "d",
	})
	return insts, v
}

func TestSchemaHandler_GetDB_BYOCDialsThroughGuard(t *testing.T) {
	t.Setenv("SCHEMA_DB_SSLMODE", "disable")
	insts, v := byocDialFixture(domain.ModeBYOC)
	h := NewSchemaHandler(v)
	h.SetInstanceStore(insts)

	_, err := h.getDB("proj-dial")
	if !errors.Is(err, byoc.ErrInternalAddress) {
		t.Fatalf("BYOC getDB = %v, want ErrInternalAddress", err)
	}
}

func TestSchemaHandler_GetDB_ManagedIsNotGuarded(t *testing.T) {
	t.Setenv("SCHEMA_DB_SSLMODE", "disable")
	insts, v := byocDialFixture(domain.ModeK8s)
	h := NewSchemaHandler(v)
	h.SetInstanceStore(insts)

	_, err := h.getDB("proj-dial")
	if err == nil || errors.Is(err, byoc.ErrInternalAddress) {
		t.Fatalf("managed getDB = %v, want a plain connection error", err)
	}
}

func TestRealtimeHandler_Dial_BYOCDialsThroughGuard(t *testing.T) {
	insts, v := byocDialFixture(domain.ModeBYOC)
	h := NewRealtimeHandler(insts, nil, v)

	req := httptest.NewRequest("GET", "/api/projects/proj-dial/realtime/tables", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("projectId", "proj-dial")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	_, db, err := h.dial(req)
	if err != nil {
		t.Fatalf("dial = %v", err)
	}
	defer db.Close()
	if err := db.PingContext(context.Background()); !errors.Is(err, byoc.ErrInternalAddress) {
		t.Fatalf("BYOC realtime ping = %v, want ErrInternalAddress", err)
	}
}
