package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func (f *mongoEndpointFixture) ingress() ([]int, bool) {
	ports, open := f.kube.PublicDBIngress[f.inst.Namespace+"/"+f.inst.ProjectID]
	return ports, open
}

func TestAPrivateProjectOpensNoPublicIngress(t *testing.T) {
	f := newMongoEndpointFixture(t, true)
	if _, err := f.svc.Describe(context.Background(), f.inst.ProjectID); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if _, open := f.ingress(); open {
		t.Error("a project that never opted in has public ingress")
	}
}

func TestEnablingOpensTheDatabasePortsToOutsideTraffic(t *testing.T) {
	for _, tc := range []struct {
		documentDB bool
		want       []int
	}{
		{documentDB: false, want: []int{5432}},
		{documentDB: true, want: []int{5432, 10260}},
	} {
		f := newMongoEndpointFixture(t, tc.documentDB)
		if _, err := f.svc.SetPublic(context.Background(), f.inst.ProjectID, true); err != nil {
			t.Fatalf("SetPublic: %v", err)
		}
		if ports, _ := f.ingress(); !reflect.DeepEqual(ports, tc.want) {
			t.Errorf("documentDB=%v: open ports %v, want %v", tc.documentDB, ports, tc.want)
		}
	}
}

func TestDisablingClosesThePublicIngress(t *testing.T) {
	f := newMongoEndpointFixture(t, true)
	ctx := context.Background()
	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, open := f.ingress(); open {
		t.Error("public ingress is still open after the endpoint was turned off")
	}
}

func TestAPauseClosesAndAResumeReopensThePublicIngress(t *testing.T) {
	f := newMongoEndpointFixture(t, false)
	ctx := context.Background()
	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := f.svc.Withdraw(ctx, f.inst); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, open := f.ingress(); open {
		t.Error("public ingress is open on a withdrawn project")
	}
	if err := f.svc.Publish(ctx, f.inst); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, open := f.ingress(); !open {
		t.Error("public ingress did not come back with the endpoint")
	}
	if err := f.svc.Release(ctx, f.inst); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, open := f.ingress(); open {
		t.Error("public ingress is open on a released project")
	}
}

func TestEnableIsRefusedWhenTheIngressCannotBeOpened(t *testing.T) {
	f := newMongoEndpointFixture(t, false)
	refused := errors.New("refused")
	f.kube.PublicDBIngressError = refused

	if _, err := f.svc.SetPublic(context.Background(), f.inst.ProjectID, true); !errors.Is(err, refused) {
		t.Fatalf("SetPublic: got %v, want the refusal", err)
	}
	endpoint, _ := f.store.GetDatabaseEndpoint(context.Background(), f.inst.ProjectID)
	if endpoint.PublicEnabled {
		t.Error("the endpoint was recorded public although traffic cannot reach it")
	}
}

func TestDisableIsRefusedWhenTheIngressCannotBeClosed(t *testing.T) {
	f := newMongoEndpointFixture(t, false)
	ctx := context.Background()
	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	refused := errors.New("refused")
	f.kube.PublicDBIngressError = refused

	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, false); !errors.Is(err, refused) {
		t.Fatalf("disable: got %v, want the refusal", err)
	}
	endpoint, _ := f.store.GetDatabaseEndpoint(ctx, f.inst.ProjectID)
	if !endpoint.PublicEnabled {
		t.Error("the endpoint was recorded off although outside traffic is still admitted")
	}
}
