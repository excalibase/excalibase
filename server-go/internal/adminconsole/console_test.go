package adminconsole

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

type fakeReader struct {
	users   []UserRow
	core    *ProjectCore
	sites   []SiteBrief
	fns     []FunctionBrief
	audit   []AuditBrief
	lastQ   UserQuery
	overall *Overview
}

func (f *fakeReader) ListUsers(_ context.Context, q UserQuery) (Page[UserRow], error) {
	f.lastQ = q
	return Page[UserRow]{Items: f.users, Total: len(f.users)}, nil
}
func (f *fakeReader) ListOrgs(context.Context, OrgQuery) (Page[OrgRow], error) {
	return Page[OrgRow]{Items: []OrgRow{}}, nil
}
func (f *fakeReader) Overview(context.Context, time.Time) (*Overview, error) { return f.overall, nil }
func (f *fakeReader) ProjectCore(context.Context, string) (*ProjectCore, error) {
	return f.core, nil
}
func (f *fakeReader) ProjectSites(context.Context, string) ([]SiteBrief, error) { return f.sites, nil }
func (f *fakeReader) ProjectFunctions(context.Context, string) ([]FunctionBrief, error) {
	return f.fns, nil
}
func (f *fakeReader) ProjectAudit(context.Context, string, int) ([]AuditBrief, error) {
	return f.audit, nil
}

type fakeAppLister struct{ apps []*apphost.App }

func (f fakeAppLister) List(string) ([]*apphost.App, error) { return f.apps, nil }

type fakeProbe struct {
	replicas map[string]int32
	err      error
}

func (p fakeProbe) AppAvailableReplicas(_ context.Context, _, name string) (int32, error) {
	return p.replicas[name], p.err
}

func TestUsersAddTheTempMailFlagFromTheAddressList(t *testing.T) {
	reader := &fakeReader{users: []UserRow{{ID: "u1", Email: "a@mailinator.test"}, {ID: "u2", Email: "b@example.test"}}}
	console := NewConsole(reader, nil, nil)
	console.SetTempMailCheck(func(email string) bool { return email == "a@mailinator.test" })

	page, err := console.Users(context.Background(), UserQuery{Search: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].TempMail == nil || !*page.Items[0].TempMail || page.Items[1].TempMail == nil || *page.Items[1].TempMail {
		t.Fatalf("temp-mail flags = %v %v", page.Items[0].TempMail, page.Items[1].TempMail)
	}
}

func TestUsersLeaveTempMailUnknownWithoutAList(t *testing.T) {
	reader := &fakeReader{users: []UserRow{{ID: "u1", Email: "a@x.test"}}}
	page, err := NewConsole(reader, nil, nil).Users(context.Background(), UserQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].TempMail != nil {
		t.Fatal("without a list the flag must be absent, not false")
	}
}

func TestUsersClampThePageSize(t *testing.T) {
	reader := &fakeReader{}
	console := NewConsole(reader, nil, nil)
	if _, err := console.Users(context.Background(), UserQuery{Limit: 100000, Offset: -5}); err != nil {
		t.Fatal(err)
	}
	if reader.lastQ.Limit != MaxPageSize || reader.lastQ.Offset != 0 {
		t.Fatalf("query = %+v", reader.lastQ)
	}
	if _, err := console.Users(context.Background(), UserQuery{}); err != nil {
		t.Fatal(err)
	}
	if reader.lastQ.Limit != DefaultPageSize {
		t.Fatalf("default limit = %d", reader.lastQ.Limit)
	}
}

func TestProjectDetailReadsRunningStateFromTheRuntime(t *testing.T) {
	reader := &fakeReader{core: &ProjectCore{ProjectID: "p1", Namespace: "ns-p1", OwnerEmail: "o@x.test"}}
	apps := fakeAppLister{apps: []*apphost.App{
		{ID: "a1", Name: "web", Image: "nginx:1", Replicas: 2, Status: apphost.StatusRunning, Tier: domain.Free},
		{ID: "a2", Name: "gone", Image: "x:1", Replicas: 1, Status: apphost.StatusRunning, Tier: domain.Free},
	}}
	probe := fakeProbe{replicas: map[string]int32{"app-web": 2, "app-gone": 0}}
	console := NewConsole(reader, apps, probe)
	console.SetObjectName(func(name string) string { return "app-" + name })

	detail, err := console.Project(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Apps) != 2 {
		t.Fatalf("apps = %+v", detail.Apps)
	}
	if detail.Apps[0].Running == nil || *detail.Apps[0].Running != 2 || detail.Apps[0].Image != "nginx:1" {
		t.Fatalf("web = %+v", detail.Apps[0])
	}
	if detail.Apps[1].Running == nil || *detail.Apps[1].Running != 0 || detail.Apps[1].Status != apphost.StatusRunning {
		t.Fatalf("an app recorded running with no pods must show 0 running: %+v", detail.Apps[1])
	}
}

func TestProjectDetailReportsUnknownWhenTheRuntimeCannotBeAsked(t *testing.T) {
	reader := &fakeReader{core: &ProjectCore{ProjectID: "p1", Namespace: "ns-p1"}}
	apps := fakeAppLister{apps: []*apphost.App{{ID: "a1", Name: "web", Replicas: 1, Status: apphost.StatusRunning}}}
	console := NewConsole(reader, apps, fakeProbe{err: errors.New("api down")})

	detail, err := console.Project(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Apps[0].Running != nil {
		t.Fatal("running must be absent when the cluster did not answer")
	}
}

func TestProjectDetailUnknownProject(t *testing.T) {
	if _, err := NewConsole(&fakeReader{}, nil, nil).Project(context.Background(), "nope"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("err = %v", err)
	}
}
