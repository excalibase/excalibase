package service

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// newMySQLEndpointFixture is a MySQL project (ADR 0041) on a platform that
// publishes database ports. MOCO serves no CA a client could verify against.
func newMySQLEndpointFixture(t *testing.T) *mongoEndpointFixture {
	t.Helper()
	port := 3306
	inst := &domain.DatabaseInstance{
		ProjectID: "proj-my0001", Namespace: "org-a-proj-my0001", DBType: domain.MySQL,
		Host:         k8s.MySQLPrimaryHost("proj-my0001", "org-a-proj-my0001"),
		Port:         &port,
		DatabaseName: "app", Username: "admin", SSLMode: domain.MySQLSSLMode,
		DeploymentMode: domain.ModeK8s, Status: "ACTIVE",
	}
	kube := k8s.NewMockClient()
	window, err := domain.NewPortRange(32000, 32099)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	svc := NewDBEndpointService(DBEndpointServiceConfig{
		Endpoints: newFakeEndpointStore(), Instances: staticInstances{inst}, Kube: kube,
		DomainSuffix: "db.example.com", Ports: window, Quarantine: time.Hour, SharedIPKey: "excalibase-db-edge",
	})
	return &mongoEndpointFixture{svc: svc, kube: kube, inst: inst}
}

func TestPublishingAMySQLProjectFrontsMOCOsPrimaryOn3306(t *testing.T) {
	f := newMySQLEndpointFixture(t)
	view, err := f.svc.SetPublic(context.Background(), f.inst.ProjectID, true)
	if err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	spec, ok := f.kube.PublicDBServices[f.inst.Namespace+"/"+f.inst.ProjectID+"-mysql-public"]
	if !ok || spec.ReadWriteService != "moco-proj-my0001-mysql-primary" || spec.TargetPort != 3306 ||
		spec.PortName != "mysql" || spec.Port != view.Port {
		t.Errorf("service %+v", spec)
	}
	if ports, _ := f.ingress(); !reflect.DeepEqual(ports, []int{3306}) {
		t.Errorf("ingress ports %v", ports)
	}
	if selector := f.kube.PublicDBIngressSelectors[f.inst.Namespace+"/"+f.inst.ProjectID]; !reflect.DeepEqual(selector,
		map[string]string{"app.kubernetes.io/created-by": "moco", "app.kubernetes.io/name": "mysql",
			"app.kubernetes.io/instance": "proj-my0001-mysql"}) {
		t.Errorf("ingress selector %v", selector)
	}
}

func TestAMySQLEndpointAlwaysRequiresTLS(t *testing.T) {
	f := newMySQLEndpointFixture(t)
	view, err := f.svc.SetPublic(context.Background(), f.inst.ProjectID, true)
	if err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	host, _ := domain.DBEndpointHost(f.inst.ProjectID, "db.example.com")
	// EXC-589: the strings verify the server, and the CA they verify it with is handed out.
	if !view.RequireTLS || view.Engine != "mysql" || view.CACertificate != string(k8s.MockMySQLCA()["tls.crt"]) || view.Connection.AllowPlaintext != "" ||
		view.Connection.RequireTLS != "mysql://admin@"+host+":"+strconv.Itoa(view.Port)+"/app?ssl-mode=VERIFY_IDENTITY" {
		t.Errorf("public view %+v", view)
	}
	if view.Internal.Port != 3306 || view.Internal.ConnectionString != "mysql://admin@"+f.inst.Host+":3306/app?ssl-mode=VERIFY_IDENTITY" {
		t.Errorf("internal %+v", view.Internal)
	}
	for _, want := range []bool{false, true} {
		if _, err := f.svc.SetRequireTLS(context.Background(), f.inst.ProjectID, want); !errors.Is(err, ErrNotForMySQL) {
			t.Errorf("SetRequireTLS(%v) = %v, want ErrNotForMySQL", want, err)
		}
	}
}

func TestAMySQLEndpointIsWithdrawnOnPauseAndBackOnResume(t *testing.T) {
	f := newMySQLEndpointFixture(t)
	ctx := context.Background()
	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	name := f.inst.Namespace + "/" + f.inst.ProjectID + "-mysql-public"
	if err := f.svc.Withdraw(ctx, f.inst); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, ok := f.kube.PublicDBServices[name]; ok {
		t.Error("the Service survived the pause")
	}
	if err := f.svc.Publish(ctx, f.inst); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if spec := f.kube.PublicDBServices[name]; spec.TargetPort != 3306 {
		t.Errorf("resumed service %+v", spec)
	}
}

// A deleted LoadBalancer Service can linger while a cloud controller clears
// its finalizer (k3s does). Withdrawing waits for it to be observed gone, and
// still refuses when it never goes.
func TestWithdrawWaitsForALingeringService(t *testing.T) {
	for _, tc := range []struct {
		lingers int
		wantErr error
	}{{lingers: 3}, {lingers: 1000, wantErr: ErrDBEndpointNotObserved}} {
		f := newMySQLEndpointFixture(t)
		f.svc.goneTimeout, f.svc.gonePoll = 50*time.Millisecond, time.Millisecond
		ctx := context.Background()
		if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true); err != nil {
			t.Fatalf("enable: %v", err)
		}
		f.kube.LingeringPublicDBServices[f.inst.Namespace+"/"+f.inst.ProjectID+"-mysql-public"] = tc.lingers
		if err := f.svc.Withdraw(ctx, f.inst); !errors.Is(err, tc.wantErr) {
			t.Errorf("lingers %d: Withdraw = %v, want %v", tc.lingers, err, tc.wantErr)
		}
	}
}
