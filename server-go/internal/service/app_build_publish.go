package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// AppImageCollector streams the archive a finished app build packed.
type AppImageCollector interface {
	Collect(ctx context.Context, buildID string, w io.Writer, limit int64) error
}

// ImagePusher uploads a checked layout to one repository and answers the digest it deploys by.
type ImagePusher interface {
	Push(ctx context.Context, repository string, layout *appregistry.Layout, at time.Time) (string, error)
}

// RegistryRoom is the project's share of the registry: Fits measures before
// the push and removes nothing; MakeRoom removes after it, never the image pushed.
type RegistryRoom interface {
	Fits(ctx context.Context, projectID string, incoming map[string]int64, quota int64) error
	MakeRoom(ctx context.Context, projectID, pushed string, quota int64) error
}

// ProjectLocker serializes a project's publishes across replicas; the returned function releases the lock.
type ProjectLocker interface {
	Lock(ctx context.Context, projectID string) (func(), error)
}

// AppImageDeployer is the deploy the publish hands the image to.
type AppImageDeployer interface {
	DeployImage(ctx context.Context, projectID, appID, image string, origin apphost.DeployOrigin) (*apphost.Deploy, error)
}

// ErrImageTooLarge fails a build whose image is past the plan's app_build_max_image.
var ErrImageTooLarge = errors.New("the built image is larger than this plan allows")

// AppBuildPublisher takes a finished build's image into the platform's
// registry and deploys it by digest (EXC-547 step 3). The archive came from
// a pod that ran the customer's code, so it is read as hostile on the way in.
type AppBuildPublisher struct {
	internal  *appregistry.Internal
	collector AppImageCollector
	pusher    ImagePusher
	room      RegistryRoom
	lock      ProjectLocker
	deployer  AppImageDeployer
	spoolDir  string
	now       func() time.Time
}

// AppBuildResult is what a finished build hands over.
type AppBuildResult struct {
	BuildID, ProjectID, CommitSHA string
	App                           *apphost.App
	// MaxImage is the plan's app_build_max_image, snapshotted when the build queued.
	MaxImage int64
}

// NewAppBuildPublisher spools archives under spoolDir (provisioning's own disk); each is removed after its push.
func NewAppBuildPublisher(internal *appregistry.Internal, collector AppImageCollector, pusher ImagePusher, room RegistryRoom, lock ProjectLocker, deployer AppImageDeployer, spoolDir string) *AppBuildPublisher {
	return &AppBuildPublisher{internal: internal, collector: collector, pusher: pusher, room: room, lock: lock, deployer: deployer, spoolDir: spoolDir, now: time.Now}
}

// Publish collects, checks and pushes the build's image, then deploys it as a git build.
func (p *AppBuildPublisher) Publish(ctx context.Context, result AppBuildResult) (*apphost.Deploy, error) {
	if result.App == nil || result.App.ProjectID != result.ProjectID || result.MaxImage <= 0 {
		return nil, errors.New("a publish names the project's app and the plan's image limit")
	}
	repository, err := appregistry.Repository(result.ProjectID, result.App.Name)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(p.spoolDir, "app-image-")
	if err != nil {
		return nil, fmt.Errorf("spool the build's image: %w", err)
	}
	defer os.RemoveAll(dir)
	layout, err := p.collect(ctx, result, dir)
	if err != nil {
		return nil, err
	}
	return p.pushAndDeploy(ctx, result, repository, layout)
}

// pushAndDeploy holds the project's lock from the quota measure to the room
// made, so two publishes never pass the quota on one measure. Room is made
// last: a push or deploy record that fails leaves every older image, and the
// deploy recorded pending keeps the new one.
func (p *AppBuildPublisher) pushAndDeploy(ctx context.Context, result AppBuildResult, repository string, layout *appregistry.Layout) (*apphost.Deploy, error) {
	unlock, err := p.lock.Lock(ctx, result.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("lock the project's registry space: %w", err)
	}
	defer unlock()
	quota := RegistryQuotaImages * result.MaxImage
	if err := p.room.Fits(ctx, result.ProjectID, layout.Contents(), quota); err != nil {
		return nil, err
	}
	digest, err := p.pusher.Push(ctx, repository, layout, p.now())
	if err != nil {
		return nil, fmt.Errorf("push the build's image: %w", err)
	}
	image, err := p.internal.Image(result.ProjectID, result.App.Name, digest)
	if err != nil {
		return nil, err
	}
	origin := apphost.DeployOrigin{Actor: "git-build", Source: apphost.DeploySourceGitBuild, CommitSHA: result.CommitSHA}
	deploy, err := p.deployer.DeployImage(ctx, result.ProjectID, result.App.ID, image, origin)
	if err != nil {
		return nil, err
	}
	// The quota is a soft bound: a room not made now is the next publish's or the sweep's.
	if err := p.room.MakeRoom(ctx, result.ProjectID, digest, quota); err != nil {
		log.Printf("app build %s: make room in the registry: %v", result.BuildID, err)
	}
	return deploy, nil
}

// collect streams the archive straight into the hostile reader: nothing is
// spooled but blobs whose bytes hash to their names, at most MaxImage of them.
func (p *AppBuildPublisher) collect(ctx context.Context, result AppBuildResult, dir string) (*appregistry.Layout, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	collected := make(chan error, 1)
	go func() {
		err := p.collector.Collect(ctx, result.BuildID, writer, result.MaxImage)
		writer.CloseWithError(err)
		collected <- err
	}()
	stream := &streamErr{r: reader}
	layout, readErr := appregistry.ReadLayout(stream, dir, result.MaxImage)
	if readErr == nil {
		// The tar's end padding follows its last entry.
		_, readErr = io.Copy(io.Discard, stream)
	}
	reader.CloseWithError(errArchiveRefused)
	collectErr := <-collected
	switch {
	case errors.Is(collectErr, sitebuilds.ErrOutputTooLarge):
		return nil, fmt.Errorf("%w (%d bytes)", ErrImageTooLarge, result.MaxImage)
	case stream.err != nil && collectErr != nil:
		// The stream broke on the collector's side, not on what it carried.
		return nil, fmt.Errorf("collect the build's image: %w", collectErr)
	case readErr != nil:
		return nil, readErr
	case collectErr != nil:
		return nil, fmt.Errorf("collect the build's image: %w", collectErr)
	}
	return layout, nil
}

var errArchiveRefused = errors.New("the image archive was refused")

// streamErr remembers a read that failed for a reason other than the end of the stream.
type streamErr struct {
	r   io.Reader
	err error
}

func (s *streamErr) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && s.err == nil {
		s.err = err
	}
	return n, err
}
