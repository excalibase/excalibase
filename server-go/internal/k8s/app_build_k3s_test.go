//go:build live

package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/excalibase/provisioning-poc/internal/appbuilds"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

const appBuildWait = 20 * time.Minute

// TestK3sAppBuild runs git builds of apps (EXC-547) through the real Job under
// gVisor, in a namespace held to PSA baseline: a Dockerfile with a secret
// mount and a Buildpacks build each make an OCI image archive, a missing
// Dockerfile and an out-of-memory build are named, and the fence holds a
// private address and the metadata address while the build runs.
func TestK3sAppBuild(t *testing.T) {
	cluster := startGVisorK3s(t, gvisorPlatformSystrap)
	namespace := cluster.createNamespace(t, "excalibase-app-builds")
	baseline := []byte(`{"metadata":{"labels":{"pod-security.kubernetes.io/enforce":"baseline"}}}`)
	if _, err := cluster.clientset.CoreV1().Namespaces().Patch(context.Background(), namespace, types.MergePatchType, baseline, metav1.PatchOptions{}); err != nil {
		t.Fatalf("hold the namespace to PSA baseline: %v", err)
	}
	commit := cluster.startGitServerWith(t, "app_build_node", sitebuilds.NodeImage("", "22"), config.DefaultSiteBuildPrepareImage,
		config.DefaultAppBuildKitImage, config.DefaultAppBuilderImage, config.DefaultAppCraneImage)
	cluster.allowGitServer(t, namespace)
	runner := NewAppBuildRunner(cluster.client, AppBuildOptions{
		Namespace: namespace, RuntimeClass: gvisorRuntimeClass, Slots: 4, PrepareImage: config.DefaultSiteBuildPrepareImage,
		ToolImage: sitebuilds.NodeImage("", "22"), BuildKitImage: config.DefaultAppBuildKitImage,
		BuilderImage: config.DefaultAppBuilderImage, RunImage: config.DefaultAppRunImage, CraneImage: config.DefaultAppCraneImage,
		CloneSchemes: []string{"git"},
	})
	spec := func(buildID string, builder appbuilds.Builder) appbuilds.RunSpec {
		return appbuilds.RunSpec{
			BuildID: buildID, AppID: "app-1", ProjectID: "proj-1", Builder: builder,
			CloneURL: "git://gitsrv." + gitServerNamespace + ".svc.cluster.local/site.git", Commit: commit, CloneToken: "unused-by-git-daemon",
			BuildArgs: map[string]string{"GREETING": "hello"}, Secrets: map[string]string{"NPM_TOKEN": "fixture-secret-value"},
			CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi", Timeout: 15 * time.Minute, MaxImageBytes: 2 << 30,
			LargestPlan: sitebuilds.BuildSize{CPUMillis: 2000, Memory: "4Gi", Disk: "10Gi"},
		}
	}

	t.Run("a Dockerfile builds into an OCI image with its secret as a mount", func(t *testing.T) {
		build := spec("dockerfile", appbuilds.BuilderDockerfile)
		checkImageBuild(t, runner, build, "secret mount ok", "packed the image")
	})

	t.Run("Buildpacks build the Node app with no Dockerfile in use", func(t *testing.T) {
		build := spec("buildpacks", appbuilds.BuilderBuildpacks)
		checkImageBuild(t, runner, build, "paketo-buildpacks/node-engine", "packed the image")
	})

	t.Run("a missing Dockerfile is named", func(t *testing.T) {
		build := spec("nodockerfile", appbuilds.BuilderDockerfile)
		build.DockerfilePath = "missing.Dockerfile"
		status := runAppBuildToEnd(t, runner, build)
		if status.Failure != appbuilds.FailNoDockerfile {
			t.Fatalf("status %+v\n%s", status, appBuildLogs(runner, build.BuildID))
		}
		removeAppBuild(t, runner, build.BuildID)
	})

	t.Run("an out-of-memory build is named, whatever its exit code", func(t *testing.T) {
		build := spec("oom", appbuilds.BuilderDockerfile)
		build.Memory = "1Gi"
		build.BuildArgs = map[string]string{"HOG": "1"}
		status := runAppBuildToEnd(t, runner, build)
		if status.Failure != sitebuilds.FailOutOfMemory {
			t.Fatalf("status %+v\n%s", status, appBuildLogs(runner, build.BuildID))
		}
		removeAppBuild(t, runner, build.BuildID)
	})

	t.Run("the fence holds while the image builds", func(t *testing.T) {
		build := spec("fence", appbuilds.BuilderDockerfile)
		build.BuildArgs = map[string]string{"PROBE": "1"}
		status := runAppBuildToEnd(t, runner, build)
		logs := appBuildLogs(runner, build.BuildID)
		if status.Phase != sitebuilds.RunReady || !strings.Contains(logs, "fence held") {
			t.Fatalf("status %+v\n%s", status, logs)
		}
		removeAppBuild(t, runner, build.BuildID)
	})
}

func checkImageBuild(t *testing.T, runner *AppBuildRunner, build appbuilds.RunSpec, wantLogs ...string) {
	t.Helper()
	status := runAppBuildToEnd(t, runner, build)
	logs := appBuildLogs(runner, build.BuildID)
	if status.Phase != sitebuilds.RunReady {
		t.Fatalf("status %+v\n%s", status, logs)
	}
	var archive bytes.Buffer
	if err := runner.Collect(context.Background(), build.BuildID, &archive, build.MaxImageBytes); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !ociArchive(t, archive.Bytes()) {
		t.Error("the collected archive is not an OCI image layout")
	}
	var small bytes.Buffer
	if err := runner.Collect(context.Background(), build.BuildID, &small, 1024); !errors.Is(err, sitebuilds.ErrOutputTooLarge) {
		t.Errorf("an image past the plan's cap must be refused: %v", err)
	}
	for _, want := range append([]string{"network fence up", "checked out " + build.Commit}, wantLogs...) {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
	for _, secret := range []string{build.CloneToken, "fixture-secret-value"} {
		if strings.Contains(logs, secret) {
			t.Errorf("%q reached the build log", secret)
		}
	}
	removeAppBuild(t, runner, build.BuildID)
}

// ociArchive reports an index.json and an oci-layout file in the tar.
func ociArchive(t *testing.T, archive []byte) bool {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(archive))
	found := map[string]bool{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read the archive: %v", err)
		}
		found[strings.TrimPrefix(header.Name, "./")] = true
	}
	return found["index.json"] && found["oci-layout"]
}

func runAppBuildToEnd(t *testing.T, runner *AppBuildRunner, build appbuilds.RunSpec) sitebuilds.RunStatus {
	t.Helper()
	ctx := context.Background()
	if err := runner.Start(ctx, build); err != nil {
		t.Fatalf("start %s: %v", build.BuildID, err)
	}
	deadline := time.Now().Add(appBuildWait)
	for time.Now().Before(deadline) {
		status, err := runner.Status(ctx, build.BuildID)
		if err != nil {
			t.Fatalf("status %s: %v", build.BuildID, err)
		}
		if status.Phase == sitebuilds.RunReady || status.Phase == sitebuilds.RunFailed {
			return status
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("build %s did not finish in %s\n%s", build.BuildID, appBuildWait, appBuildLogs(runner, build.BuildID))
	return sitebuilds.RunStatus{}
}

func appBuildLogs(runner *AppBuildRunner, buildID string) string {
	logs, err := runner.Logs(context.Background(), buildID)
	if err != nil {
		return "logs: " + err.Error()
	}
	return logs
}

func removeAppBuild(t *testing.T, runner *AppBuildRunner, buildID string) {
	t.Helper()
	if err := runner.Remove(context.Background(), buildID); err != nil {
		t.Errorf("remove %s: %v", buildID, err)
	}
}
