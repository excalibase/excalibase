package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

type fakeVolumeReader struct {
	volumes []string
	err     error
	asked   []string
}

func (f *fakeVolumeReader) Volumes(_ context.Context, image string, _ *apphost.RegistryCredential) ([]string, error) {
	f.asked = append(f.asked, image)
	return f.volumes, f.err
}

// EXC-732: the image's VOLUME paths are read when it deploys and each becomes a capped emptyDir.
func TestDeployApp_CapsTheImagesDeclaredVolumes(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	reader := &fakeVolumeReader{volumes: []string{"/var/lib/app"}}
	svc.SetImageVolumes(reader)

	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if len(reader.asked) != 1 {
		t.Fatalf("the registry was asked %d times, want once: %v", len(reader.asked), reader.asked)
	}
	mounts := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Spec.Containers[0].VolumeMounts
	if !slices.ContainsFunc(mounts, func(m corev1.VolumeMount) bool { return m.MountPath == "/var/lib/app" }) {
		t.Fatalf("mounts %+v: no capped directory at the image's VOLUME", mounts)
	}
}

// An image whose volumes cannot be read is not run: they would be uncapped space on the node.
func TestDeployApp_FailsWhenTheImagesVolumesCannotBeRead(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.SetImageVolumes(&fakeVolumeReader{err: errors.New("registry down")})

	returned, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if got := deploys.getByID(returned.ID).Status; got != apphost.DeployStatusFailed {
		t.Fatalf("status %q, want failed", got)
	}
	if _, applied := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]; applied {
		t.Fatal("a workload was applied for an image whose volumes are unknown")
	}
}
