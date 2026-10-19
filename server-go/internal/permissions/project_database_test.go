package permissions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

type enginePools struct{ mysqlAsked bool }

func (p *enginePools) Open(_ context.Context, projectID string) (*sql.DB, error) {
	return nil, fmt.Errorf("%w: %s", domain.ErrPostgresOnly, projectID)
}

func (p *enginePools) OpenMySQL(context.Context, string, bool) (*sql.DB, error) {
	p.mysqlAsked = true
	return nil, errors.New("mysql pool unavailable")
}

// A MySQL project's schema is read through its MySQL pool.
func TestLiveSchemaReadsAMySQLProjectThroughItsPool(t *testing.T) {
	pools := &enginePools{}
	_, err := NewProjectDatabase(pools, schema.NewIntrospector()).LiveSchema(context.Background(), "proj-1", "app")
	if !pools.mysqlAsked || err == nil || err.Error() != "mysql pool unavailable" {
		t.Fatalf("mysqlAsked=%v err=%v", pools.mysqlAsked, err)
	}
}

type postgresPools struct{ err error }

func (p postgresPools) Open(context.Context, string) (*sql.DB, error) { return nil, p.err }

func (p postgresPools) OpenMySQL(context.Context, string, bool) (*sql.DB, error) {
	return nil, errors.New("not asked")
}

// A Postgres project's failure to open is returned as it is.
func TestLiveSchemaPassesAPostgresOpenFailureOn(t *testing.T) {
	want := errors.New("project is not servable")
	_, err := NewProjectDatabase(postgresPools{err: want}, schema.NewIntrospector()).LiveSchema(context.Background(), "proj-1", "public")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}
