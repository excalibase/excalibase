package adminconsole

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

var errBoom = errors.New("store down")

// brokenReader fails the one read named in fail and answers the rest.
type brokenReader struct {
	*fakeReader
	fail string
}

func (b brokenReader) hit(name string) error {
	if b.fail == name {
		return errBoom
	}
	return nil
}
func (b brokenReader) ListUsers(ctx context.Context, q UserQuery) (Page[UserRow], error) {
	if err := b.hit("users"); err != nil {
		return Page[UserRow]{}, err
	}
	return b.fakeReader.ListUsers(ctx, q)
}
func (b brokenReader) ListOrgs(ctx context.Context, q OrgQuery) (Page[OrgRow], error) {
	if err := b.hit("orgs"); err != nil {
		return Page[OrgRow]{}, err
	}
	return b.fakeReader.ListOrgs(ctx, q)
}
func (b brokenReader) Overview(ctx context.Context, since time.Time) (*Overview, error) {
	if err := b.hit("overview"); err != nil {
		return nil, err
	}
	return b.fakeReader.Overview(ctx, since)
}
func (b brokenReader) ProjectCore(ctx context.Context, id string) (*ProjectCore, error) {
	if err := b.hit("core"); err != nil {
		return nil, err
	}
	return b.fakeReader.ProjectCore(ctx, id)
}
func (b brokenReader) ProjectSites(ctx context.Context, id string) ([]SiteBrief, error) {
	if err := b.hit("sites"); err != nil {
		return nil, err
	}
	return b.fakeReader.ProjectSites(ctx, id)
}
func (b brokenReader) ProjectFunctions(ctx context.Context, id string) ([]FunctionBrief, error) {
	if err := b.hit("functions"); err != nil {
		return nil, err
	}
	return b.fakeReader.ProjectFunctions(ctx, id)
}
func (b brokenReader) ProjectAudit(ctx context.Context, id string, n int) ([]AuditBrief, error) {
	if err := b.hit("audit"); err != nil {
		return nil, err
	}
	return b.fakeReader.ProjectAudit(ctx, id, n)
}

type brokenApps struct{}

func (brokenApps) List(string) ([]*apphost.App, error) { return nil, errBoom }

// A failed read is an error the caller can see, never an empty answer.
func TestConsoleSurfacesEveryFailedRead(t *testing.T) {
	ctx := context.Background()
	core := &ProjectCore{ProjectID: "p1", Namespace: "ns"}
	for _, step := range []string{"core", "sites", "functions", "audit"} {
		console := NewConsole(brokenReader{&fakeReader{core: core}, step}, nil, nil)
		if _, err := console.Project(ctx, admin, "p1", "checking the abuse report"); !errors.Is(err, errBoom) {
			t.Errorf("project read %s: err = %v, want the store failure", step, err)
		}
	}
	apps := NewConsole(brokenReader{&fakeReader{core: core}, ""}, brokenApps{}, nil)
	if _, err := apps.Project(ctx, admin, "p1", "checking the abuse report"); !errors.Is(err, errBoom) {
		t.Errorf("app listing: err = %v", err)
	}

	list := NewConsole(brokenReader{&fakeReader{}, "users"}, nil, nil)
	if _, err := list.Users(ctx, UserQuery{}); !errors.Is(err, errBoom) {
		t.Errorf("users: err = %v", err)
	}
	orgs := NewConsole(brokenReader{&fakeReader{}, "orgs"}, nil, nil)
	if _, err := orgs.Orgs(ctx, OrgQuery{}); !errors.Is(err, errBoom) {
		t.Errorf("orgs: err = %v", err)
	}
	overview := NewConsole(brokenReader{&fakeReader{}, "overview"}, nil, nil)
	if _, err := overview.Overview(ctx); !errors.Is(err, errBoom) {
		t.Errorf("overview: err = %v", err)
	}
}

type failingAuditLog struct{}

func (failingAuditLog) LogAudit(context.Context, *domain.AuditEntry) error { return errBoom }

// A down audit table is logged, never a reason to hide a failing project.
func TestProjectStillOpensWhenTheOpenAuditWriteFails(t *testing.T) {
	console := NewConsole(&fakeReader{core: &ProjectCore{ProjectID: "p1"}}, nil, nil)
	console.SetAudit(failingAuditLog{})
	if _, err := console.Project(context.Background(), admin, "p1", "checking the abuse report"); err != nil {
		t.Fatalf("a failed audit write must not fail the read: %v", err)
	}
}

func TestPodSizeIsZeroForAnUnknownTier(t *testing.T) {
	if cpu, memory := podSize(&apphost.App{Tier: "NOT_A_TIER"}); cpu != 0 || memory != 0 {
		t.Fatalf("got %d, %d", cpu, memory)
	}
}
