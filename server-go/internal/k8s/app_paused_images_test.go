package k8s

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

func TestPausedAppImages_ReadsTheImagesTheDeploymentWouldRun(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	dep := readDeployment(t, c, app)
	dep.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "init", Image: "ttl.sh/test-init:1h"}}
	if _, err := c.clientset.AppsV1().Deployments(testNamespace).Update(context.Background(), dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	images, err := c.PausedAppImages(context.Background(), testNamespace, app.ID, app.Name)

	if err != nil {
		t.Fatalf("PausedAppImages: %v", err)
	}
	if !slices.Contains(images, app.Image) || !slices.Contains(images, "ttl.sh/test-init:1h") {
		t.Errorf("images = %v, want the app image and the init container's", images)
	}
}

func TestPausedAppImages_ReadsTheDeploymentNotTheRecord(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatal(err)
	}
	dep := readDeployment(t, c, app)
	dep.Spec.Template.Spec.Containers[0].Image = "ttl.sh/test:1h"
	if _, err := c.clientset.AppsV1().Deployments(testNamespace).Update(context.Background(), dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	images, err := c.PausedAppImages(context.Background(), testNamespace, app.ID, app.Name)
	if err != nil || !slices.Contains(images, "ttl.sh/test:1h") {
		t.Fatalf("images = %v, %v; want the Deployment's own image", images, err)
	}
}

func TestPausedAppImages_RefusesWhatIsNotPausedOrNotThere(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)

	if _, err := c.PausedAppImages(context.Background(), testNamespace, app.ID, app.Name); !errors.Is(err, ErrAppNotPaused) {
		t.Fatalf("running: err = %v, want ErrAppNotPaused", err)
	}
	if _, err := c.PausedAppImages(context.Background(), testNamespace, "app-missing", "missing"); !errors.Is(err, ErrAppNotDeployed) {
		t.Fatalf("missing: err = %v, want ErrAppNotDeployed", err)
	}
}

func TestPausedAppImages_ADeploymentNamingNoImageFails(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
		t.Fatal(err)
	}
	dep := readDeployment(t, c, app)
	dep.Spec.Template.Spec.Containers = nil
	if _, err := c.clientset.AppsV1().Deployments(testNamespace).Update(context.Background(), dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	if images, err := c.PausedAppImages(context.Background(), testNamespace, app.ID, app.Name); !errors.Is(err, ErrAppImageUnreadable) {
		t.Fatalf("images = %v, want an error: an unreadable image cannot be cleared", images)
	}
}

func TestPausedAppImages_AnUnreadableDeploymentFails(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	app := fullApp()
	deployedApp(t, c, app)
	clientset.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})

	_, err := c.PausedAppImages(context.Background(), testNamespace, app.ID, app.Name)
	if err == nil || errors.Is(err, ErrAppNotDeployed) {
		t.Fatalf("err = %v, want the read failure", err)
	}
}

func TestPausedAppImages_ABlankImageIsUnreadable(t *testing.T) {
	for _, blank := range []string{"", "  "} {
		c, _ := newLifecycleFakeClient()
		app := fullApp()
		deployedApp(t, c, app)
		if err := c.PauseAppWorkload(context.Background(), testNamespace, app.ID); err != nil {
			t.Fatal(err)
		}
		dep := readDeployment(t, c, app)
		dep.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "init", Image: blank}}
		if _, err := c.clientset.AppsV1().Deployments(testNamespace).Update(context.Background(), dep, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}

		images, err := c.PausedAppImages(context.Background(), testNamespace, app.ID, app.Name)

		if !errors.Is(err, ErrAppImageUnreadable) || len(images) != 0 {
			t.Errorf("blank %q: images = %v, err = %v; want ErrAppImageUnreadable", blank, images, err)
		}
	}
}
