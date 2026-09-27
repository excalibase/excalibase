package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/go-chi/chi/v5"
)

// fakeProjectDB records what the schema handler asks of its opener.
type fakeProjectDB struct {
	openErr  error
	opened   []string
	evicted  []string
	statuses map[string]string
}

func (f *fakeProjectDB) Open(_ context.Context, projectID string) (*sql.DB, error) {
	f.opened = append(f.opened, projectID)
	if f.openErr != nil {
		return nil, f.openErr
	}
	return sql.Open("postgres", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
}

func (f *fakeProjectDB) Evict(projectID string) { f.evicted = append(f.evicted, projectID) }

func (f *fakeProjectDB) ProjectStatusChanged(projectID, status string) {
	if f.statuses == nil {
		f.statuses = map[string]string{}
	}
	f.statuses[projectID] = status
}

// EXC-431: a project claimed for teardown must not keep a pool on its database.
func TestProjectDeletingEvictsTheProjectsPool(t *testing.T) {
	pools := &fakeProjectDB{}
	var observer interface{ ProjectDeleting(string) } = NewSchemaHandler(pools, time.Second)
	observer.ProjectDeleting("proj-1")
	if len(pools.evicted) != 1 || pools.evicted[0] != "proj-1" {
		t.Fatalf("evicted: %v", pools.evicted)
	}
}

// A paused project's database is down; its pool must not hold connections.
func TestProjectStatusChangesReachThePools(t *testing.T) {
	pools := &fakeProjectDB{}
	var observer interface{ ProjectStatusChanged(string, string) } = NewSchemaHandler(pools, time.Second)
	observer.ProjectStatusChanged("proj-1", "paused")
	if pools.statuses["proj-1"] != "paused" {
		t.Fatalf("statuses: %v", pools.statuses)
	}
}

func TestSchemaHandlerOpensProjectsThroughTheSharedOpener(t *testing.T) {
	pools := &fakeProjectDB{}
	h := NewSchemaHandler(pools, time.Second)
	if _, err := h.getDB("proj-1"); err != nil {
		t.Fatal(err)
	}
	if len(pools.opened) != 1 || pools.opened[0] != "proj-1" {
		t.Fatalf("opened: %v", pools.opened)
	}
}

func TestSchemaHandlerAnswersForProjectsItMayNotReach(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: proj-1 is paused", projectdb.ErrNotServable), http.StatusConflict},
		{errors.New("unknown project proj-1"), http.StatusNotFound},
	}
	for _, c := range cases {
		h := NewSchemaHandler(&fakeProjectDB{openErr: c.err}, time.Second)
		r := chi.NewRouter()
		r.Route("/schema", h.Routes)
		if w := doRequest(r, "GET", "/schema/proj-1/tables", ""); w.Code != c.want {
			t.Errorf("%v: got %d, want %d", c.err, w.Code, c.want)
		}
	}
}
