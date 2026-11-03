package service

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/testutil/appregistrytest"
)

type fakeCollector struct {
	archive []byte
	err     error
}

func (f fakeCollector) Collect(_ context.Context, _ string, w io.Writer, limit int64) error {
	if int64(len(f.archive)) > limit {
		return sitebuilds.ErrOutputTooLarge
	}
	if _, err := w.Write(f.archive); err != nil {
		return err
	}
	return f.err
}

// journal is the order the publish's steps ran in.
type journal struct {
	mu    sync.Mutex
	steps []string
}

func (j *journal) add(step string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.steps = append(j.steps, step)
}

// streamingCollector writes its archive in chunks under a cap, the way the
// real collector streams a pod's output, so the reader can refuse it midway.
type streamingCollector struct{ archive []byte }

func (f streamingCollector) Collect(_ context.Context, _ string, w io.Writer, limit int64) error {
	for offset := 0; offset < len(f.archive); offset += 4096 {
		chunk := f.archive[offset:min(offset+4096, len(f.archive))]
		if int64(offset+len(chunk)) > limit {
			return sitebuilds.ErrOutputTooLarge
		}
		if _, err := w.Write(chunk); err != nil {
			return fmt.Errorf("read the packed output: %w", err)
		}
	}
	return nil
}

type fakePusher struct {
	repository string
	err        error
	pushed     int
	journal    *journal
}

func (f *fakePusher) Push(_ context.Context, repository string, layout *appregistry.Layout, _ time.Time) (string, error) {
	f.repository, f.pushed = repository, f.pushed+1
	f.journal.add("push")
	return string(layout.Root.Digest), f.err
}

type fakeRoom struct {
	fitErr, makeErr error
	quota           int64
	incoming        int
	pushed          string
	journal         *journal
}

func (f *fakeRoom) Fits(_ context.Context, _ string, incoming map[string]int64, quota int64) error {
	f.quota, f.incoming = quota, len(incoming)
	f.journal.add("fits")
	return f.fitErr
}

func (f *fakeRoom) MakeRoom(_ context.Context, _, pushed string, _ int64) error {
	f.pushed = pushed
	f.journal.add("make room")
	return f.makeErr
}

type fakeImageDeployer struct {
	image   string
	origin  apphost.DeployOrigin
	err     error
	journal *journal
}

func (f *fakeImageDeployer) DeployImage(_ context.Context, _, _, image string, origin apphost.DeployOrigin) (*apphost.Deploy, error) {
	f.image, f.origin = image, origin
	f.journal.add("deploy")
	if f.err != nil {
		return nil, f.err
	}
	return &apphost.Deploy{Image: image}, nil
}

// fakeLocker is one lock per project, as the platform database's advisory lock is.
type fakeLocker struct {
	mu      sync.Mutex
	held    map[string]*sync.Mutex
	err     error
	journal *journal
}

func (f *fakeLocker) Lock(ctx context.Context, projectID string) (func(), error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	if f.held == nil {
		f.held = map[string]*sync.Mutex{}
	}
	if f.held[projectID] == nil {
		f.held[projectID] = &sync.Mutex{}
	}
	project := f.held[projectID]
	f.mu.Unlock()
	project.Lock()
	f.journal.add("lock")
	return func() {
		f.journal.add("unlock")
		project.Unlock()
	}, nil
}

func publishFixture(t *testing.T, collector AppImageCollector) (*AppBuildPublisher, *fakePusher, *fakeImageDeployer, string) {
	t.Helper()
	spool := t.TempDir()
	steps := &journal{}
	pusher, deployer := &fakePusher{journal: steps}, &fakeImageDeployer{journal: steps}
	room, locker := &fakeRoom{journal: steps}, &fakeLocker{journal: steps}
	return NewAppBuildPublisher(appregistrytest.New(t), collector, pusher, room, locker, deployer, spool), pusher, deployer, spool
}

func buildResult() AppBuildResult {
	app := sampleDeployApp()
	return AppBuildResult{BuildID: "b1", ProjectID: app.ProjectID, CommitSHA: "9fceb02", App: app, MaxImage: 1 << 20}
}

func TestPublish_PushesTheImageAndDeploysItAsAGitBuild(t *testing.T) {
	// A tar's end padding beyond its last entry is read and dropped.
	archive := append(appregistrytest.Archive(t), make([]byte, 20_000)...)
	publisher, pusher, deployer, spool := publishFixture(t, fakeCollector{archive: archive})
	result := buildResult()
	deploy, err := publisher.Publish(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := appregistrytest.Host + "/" + result.ProjectID + "/" + result.App.Name + "@sha256:"
	if pusher.repository != result.ProjectID+"/"+result.App.Name || !strings.HasPrefix(deployer.image, wantPrefix) || deploy.Image != deployer.image {
		t.Fatalf("pushed to %s, deployed %s", pusher.repository, deployer.image)
	}
	if deployer.origin.Source != apphost.DeploySourceGitBuild || deployer.origin.CommitSHA != "9fceb02" {
		t.Errorf("origin %+v", deployer.origin)
	}
	room := publisher.room.(*fakeRoom)
	if room.quota != RegistryQuotaImages*result.MaxImage || room.incoming != 2 || !strings.HasSuffix(deployer.image, "@"+room.pushed) {
		t.Errorf("room asked for %d blobs within %d, kept %s", room.incoming, room.quota, room.pushed)
	}
	if left, _ := os.ReadDir(spool); len(left) != 0 {
		t.Errorf("the spool keeps %d entries", len(left))
	}
}

func TestPublish_NothingIsPushedFromAnArchiveItRefuses(t *testing.T) {
	var hostile bytes.Buffer
	tw := tar.NewWriter(&hostile)
	_ = tw.WriteHeader(&tar.Header{Name: "index.json", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"})
	_ = tw.Close()
	half := appregistrytest.Archive(t)[:1500]
	cases := map[string]struct {
		collector AppImageCollector
		want      error
	}{
		"too large":         {fakeCollector{archive: make([]byte, 2<<20)}, ErrImageTooLarge},
		"a planted symlink": {fakeCollector{archive: hostile.Bytes()}, appregistry.ErrBadArchive},
		"a broken stream":   {fakeCollector{archive: half, err: errors.New("the build pod went away")}, nil},
	}
	for name, tc := range cases {
		publisher, pusher, deployer, _ := publishFixture(t, tc.collector)
		_, err := publisher.Publish(context.Background(), buildResult())
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || pusher.pushed != 0 || deployer.image != "" {
			t.Errorf("%s: %v, pushed %d", name, err, pusher.pushed)
		}
		if tc.want == nil && !strings.Contains(err.Error(), "went away") {
			t.Errorf("%s: the collector's own failure is named: %v", name, err)
		}
	}
}

func TestPublish_AFullRegistryPushesNothing(t *testing.T) {
	publisher, pusher, deployer, _ := publishFixture(t, fakeCollector{archive: appregistrytest.Archive(t)})
	room := &fakeRoom{fitErr: &RegistryFullError{Used: 1, Incoming: 2, Quota: 2}}
	publisher.room = room
	if _, err := publisher.Publish(context.Background(), buildResult()); !errors.Is(err, ErrRegistryFull) || pusher.pushed != 0 || deployer.image != "" || room.pushed != "" {
		t.Fatalf("%v, pushed %d, made room beside %q", err, pusher.pushed, room.pushed)
	}
}

func TestPublish_AnOversizedLayerStreamedInIsTooLargeNotMalformed(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	layer := make([]byte, 2<<20)
	_ = tw.WriteHeader(&tar.Header{Name: "blobs/sha256/" + strings.Repeat("a", 64), Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(layer))})
	_, _ = tw.Write(layer)
	_ = tw.Close()
	publisher, pusher, _, _ := publishFixture(t, streamingCollector{archive: archive.Bytes()})
	_, err := publisher.Publish(context.Background(), buildResult())
	if !errors.Is(err, ErrImageTooLarge) || pusher.pushed != 0 {
		t.Fatalf("%v, pushed %d", err, pusher.pushed)
	}
}

func TestPublish_ARefusedPushDeploysNothing(t *testing.T) {
	publisher, pusher, deployer, _ := publishFixture(t, fakeCollector{archive: appregistrytest.Archive(t)})
	pusher.err = errors.New("zot down")
	if _, err := publisher.Publish(context.Background(), buildResult()); err == nil || deployer.image != "" {
		t.Fatalf("%v %s", err, deployer.image)
	}
	for name, result := range map[string]AppBuildResult{
		"no app":          {ProjectID: "p", MaxImage: 1},
		"another project": {ProjectID: "other", App: sampleDeployApp(), MaxImage: 1},
		"no image limit":  {ProjectID: sampleDeployApp().ProjectID, App: sampleDeployApp()},
	} {
		if _, err := publisher.Publish(context.Background(), result); err == nil {
			t.Errorf("%s: published", name)
		}
	}
}
