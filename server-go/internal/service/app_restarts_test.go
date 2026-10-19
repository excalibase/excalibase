package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// EXC-651: the app's restarts are read from its runtime, in its project's namespace.
func TestAppRestartsAreReadInTheProjectsNamespace(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	kube.Restarts = map[string]k8s.AppRestarts{testDeployNamespace + "/" + app.ID: {Count: 2, LastReason: "OOMKilled"}}

	restarts, err := svc.AppRestarts(context.Background(), app.ProjectID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restarts.Count != 2 || restarts.LastReason != "OOMKilled" {
		t.Fatalf("restarts = %+v", restarts)
	}
}

func TestAppRestartsOfAnUnknownAppIsNotFound(t *testing.T) {
	svc, _, _ := newDeployTestService(t, sampleDeployApp())
	if _, err := svc.AppRestarts(context.Background(), "proj", "nope"); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("err = %v, want ErrAppNotFound", err)
	}
}

func TestAppRestartsOfAProjectWithNoNamespaceIsNone(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	svc.instances = fakestore.NewInstances()
	restarts, err := svc.AppRestarts(context.Background(), app.ProjectID, app.ID)
	if err != nil || restarts.Count != 0 {
		t.Fatalf("restarts = %+v (%v), want none", restarts, err)
	}
	for _, call := range kube.Calls {
		if strings.HasPrefix(call, "AppRestarts:") {
			t.Fatalf("asked the runtime with no namespace: %v", kube.Calls)
		}
	}
}
