package service

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/testutil/appregistrytest"
)

// lockedRegistry lets publishes running at once share the fake registry.
type lockedRegistry struct {
	mu    sync.Mutex
	inner *fakeRegistryAdmin
}

func (l *lockedRegistry) Repositories(ctx context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.Repositories(ctx)
}

func (l *lockedRegistry) Tags(ctx context.Context, repository string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.inner.tags[repository]), l.inner.tagsErr
}

func (l *lockedRegistry) Untag(ctx context.Context, repository, tag string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.Untag(ctx, repository, tag)
}

func (l *lockedRegistry) Blobs(ctx context.Context, repository, reference string) (map[string]int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.Blobs(ctx, repository, reference)
}

// registryPusher tags the layout in the fake registry, after a pause that lets another publish run beside it.
type registryPusher struct {
	registry *lockedRegistry
	err      error
	pause    time.Duration
}

func (p *registryPusher) Push(_ context.Context, repository string, layout *appregistry.Layout, at time.Time) (string, error) {
	time.Sleep(p.pause)
	if p.err != nil {
		return "", p.err
	}
	digest := string(layout.Root.Digest)
	tag, err := appregistry.PushTag(digest, at)
	if err != nil {
		return "", err
	}
	p.registry.mu.Lock()
	defer p.registry.mu.Unlock()
	p.registry.inner.tags[repository] = append(p.registry.inner.tags[repository], tag)
	p.registry.inner.blobs[tag] = layout.Contents()
	return digest, nil
}

// recordingDeployer records each deploy pending, as the deploy service does.
type recordingDeployer struct{ deploys *fakeDeployStore }

func (d recordingDeployer) DeployImage(_ context.Context, projectID, appID, image string, _ apphost.DeployOrigin) (*apphost.Deploy, error) {
	deploy := &apphost.Deploy{ID: image, ProjectID: projectID, AppID: appID, Image: image, Status: apphost.DeployStatusPending}
	return deploy, d.deploys.Create(deploy)
}

const quotaMaxImage = 1 << 20

// quotaPublisher publishes into quotaFixture's registry with the real quota;
// image 5 runs and holds runningLayer bytes, images 1..4 a MiB each.
func quotaPublisher(t *testing.T, runningLayer int64) (*AppBuildPublisher, *lockedRegistry, *registryPusher) {
	t.Helper()
	fixture, registry, deploys := quotaFixture(t)
	registry.tags[quotaRepo()] = nil
	for n := 1; n <= 5; n++ {
		layer := int64(quotaMaxImage)
		if n == 5 {
			layer = runningLayer
		}
		addPush(t, registry, n, RegistryPushGrace+time.Duration(10-n)*time.Hour, layer)
	}
	locked := &lockedRegistry{inner: registry}
	sweeper := NewRegistrySweeper(locked, fixture.apps, deploys, fixture.live)
	sweeper.now = fixture.now
	pusher := &registryPusher{registry: locked}
	publisher := NewAppBuildPublisher(appregistrytest.New(t), nil, pusher, sweeper, &fakeLocker{}, recordingDeployer{deploys}, t.TempDir())
	publisher.now = func() time.Time { return sweepNow }
	return publisher, locked, pusher
}

func quotaBuild(t *testing.T, publisher *AppBuildPublisher, author string) AppBuildResult {
	t.Helper()
	publisher.collector = fakeCollector{archive: appregistrytest.ArchiveBy(t, author)}
	result := buildResult()
	result.MaxImage = quotaMaxImage
	return result
}

// imageBytes is what an archive's image holds, every blob counted.
func imageBytes(t *testing.T, author string) int64 {
	t.Helper()
	layout, err := appregistry.ReadLayout(bytes.NewReader(appregistrytest.ArchiveBy(t, author)), t.TempDir(), quotaMaxImage)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, size := range layout.Contents() {
		total += size
	}
	return total
}

func TestPublish_HoldsTheProjectLockFromTheQuotaThroughTheRoomItMakes(t *testing.T) {
	publisher, _, _, _ := publishFixture(t, fakeCollector{archive: appregistrytest.Archive(t)})
	if _, err := publisher.Publish(context.Background(), buildResult()); err != nil {
		t.Fatal(err)
	}
	want := []string{"lock", "fits", "push", "deploy", "make room", "unlock"}
	if got := publisher.room.(*fakeRoom).journal.steps; !slices.Equal(got, want) {
		t.Fatalf("steps %v, want %v", got, want)
	}
}

func TestPublish_NothingGoesWithoutTheProjectLock(t *testing.T) {
	publisher, pusher, deployer, _ := publishFixture(t, fakeCollector{archive: appregistrytest.Archive(t)})
	publisher.lock = &fakeLocker{err: errors.New("db down")}
	if _, err := publisher.Publish(context.Background(), buildResult()); err == nil || pusher.pushed != 0 || deployer.image != "" {
		t.Fatalf("%v, pushed %d", err, pusher.pushed)
	}
	if room := publisher.room.(*fakeRoom); room.incoming != 0 || room.pushed != "" {
		t.Fatal("the quota was measured without the lock")
	}
}

func TestPublish_AFailedDeployRecordMakesNoRoom(t *testing.T) {
	publisher, _, deployer, _ := publishFixture(t, fakeCollector{archive: appregistrytest.Archive(t)})
	deployer.err = errors.New("db down")
	if _, err := publisher.Publish(context.Background(), buildResult()); err == nil {
		t.Fatal("deployed")
	}
	if got := deployer.journal.steps; slices.Contains(got, "make room") || got[len(got)-1] != "unlock" {
		t.Fatalf("steps %v", got)
	}
}

// The deploy is recorded and runs: a room the registry would not make now is the next publish's or the sweep's.
func TestPublish_ARoomNotMadeAfterTheDeployKeepsTheDeploy(t *testing.T) {
	publisher, _, deployer, _ := publishFixture(t, fakeCollector{archive: appregistrytest.Archive(t)})
	publisher.room.(*fakeRoom).makeErr = errors.New("zot down")
	if deploy, err := publisher.Publish(context.Background(), buildResult()); err != nil || deploy.Image != deployer.image {
		t.Fatalf("%v", err)
	}
}

func TestPublish_AFailedPushLeavesEveryOlderTag(t *testing.T) {
	publisher, registry, pusher := quotaPublisher(t, quotaMaxImage)
	pusher.err = errors.New("zot down")
	if _, err := publisher.Publish(context.Background(), quotaBuild(t, publisher, "")); err == nil {
		t.Fatal("published")
	}
	if len(registry.inner.deleted) != 0 || len(registry.inner.tags[quotaRepo()]) != 5 {
		t.Fatalf("untagged %v before a push that failed", registry.inner.deleted)
	}
}

func TestPublish_MakesRoomAfterThePushAndKeepsTheNewImage(t *testing.T) {
	publisher, registry, _ := quotaPublisher(t, quotaMaxImage)
	deploy, err := publisher.Publish(context.Background(), quotaBuild(t, publisher, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := untaggedImages(registry.inner); !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("untagged %v, want the three oldest", got)
	}
	_, digest, _ := strings.Cut(deploy.Image, "@")
	for _, entry := range registry.inner.deleted {
		if strings.HasSuffix(entry, digest) {
			t.Fatalf("the new image lost its tag")
		}
	}
}

// Room for one more image: of two publishes at once, one measures after the
// other's push, so exactly one fits, and neither takes the other's image.
func TestPublish_ConcurrentPublishesOfAProjectNeverShareOneMeasurement(t *testing.T) {
	largest := max(imageBytes(t, "a"), imageBytes(t, "b"))
	publisher, registry, pusher := quotaPublisher(t, RegistryQuotaImages*quotaMaxImage-10-largest)
	pusher.pause = 50 * time.Millisecond
	results := map[string]AppBuildResult{}
	publishers := map[string]*AppBuildPublisher{}
	for _, author := range []string{"a", "b"} {
		// Two publishers on one registry, lock and deploys, as two replicas would be.
		copied := NewAppBuildPublisher(publisher.internal, publisher.collector, publisher.pusher, publisher.room, publisher.lock, publisher.deployer, publisher.spoolDir)
		copied.now = publisher.now
		publishers[author] = copied
		results[author] = quotaBuild(t, publishers[author], author)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var deployed []*apphost.Deploy
	var refused []error
	for author, result := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deploy, err := publishers[author].Publish(context.Background(), result)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				refused = append(refused, err)
				return
			}
			deployed = append(deployed, deploy)
		}()
	}
	wg.Wait()
	if len(deployed) != 1 || len(refused) != 1 || !errors.Is(refused[0], ErrRegistryFull) {
		t.Fatalf("deployed %d, refused %v: both passed the quota on one measurement", len(deployed), refused)
	}
	_, digest, _ := strings.Cut(deployed[0].Image, "@")
	for _, entry := range registry.inner.deleted {
		if strings.HasSuffix(entry, digest) {
			t.Fatal("the other publish took the new image")
		}
	}
}
