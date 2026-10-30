package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"

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

// RegistryRoom fits an image into the project's share of the registry.
type RegistryRoom interface {
	MakeRoom(ctx context.Context, projectID string, incoming map[string]int64, quota int64) error
}

// AppImageDeployer is the deploy the publish hands the image to.
type AppImageDeployer interface {
	DeployImage(ctx context.Context, projectID, appID, image string, origin apphost.DeployOrigin) (*apphost.Deploy, error)
}

// ErrSpoolFull refuses a publish while the spool lacks room for the plan's largest image and a margin.
var ErrSpoolFull = errors.New("the platform has no room to take in a built image right now; try the build again later")

// spoolMargin is kept free beyond the image, for the layout's own files and other publishes in flight.
const spoolMargin = 512 << 20

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
	deployer  AppImageDeployer
	spoolDir  string
	now       func() time.Time
	// free answers the bytes free on the spool's filesystem.
	free func(dir string) (int64, error)
}

// AppBuildResult is what a finished build hands over; BuildID names the attempt whose Job holds the image.
type AppBuildResult struct {
	BuildID, ProjectID, CommitSHA string
	App                           *apphost.App
	// MaxImage is the plan's app_build_max_image, snapshotted when the build queued.
	MaxImage int64
}

// NewAppBuildPublisher spools archives under spoolDir (provisioning's own disk); each is removed after its push.
func NewAppBuildPublisher(internal *appregistry.Internal, collector AppImageCollector, pusher ImagePusher, room RegistryRoom, deployer AppImageDeployer, spoolDir string) *AppBuildPublisher {
	return &AppBuildPublisher{internal: internal, collector: collector, pusher: pusher, room: room, deployer: deployer, spoolDir: spoolDir, now: time.Now, free: spoolFree}
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
	if err := p.haveRoom(result.MaxImage); err != nil {
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
	if err := p.room.MakeRoom(ctx, result.ProjectID, layout.Contents(), RegistryQuotaImages*result.MaxImage); err != nil {
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
	return p.deployer.DeployImage(ctx, result.ProjectID, result.App.ID, image, origin)
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

// haveRoom refuses before anything is collected unless the spool holds the plan's largest image and the margin.
func (p *AppBuildPublisher) haveRoom(maxImage int64) error {
	free, err := p.free(p.spoolDir)
	if err != nil {
		return fmt.Errorf("read the spool's free space: %w", err)
	}
	if free < maxImage+spoolMargin {
		return fmt.Errorf("%w (%d bytes free)", ErrSpoolFull, free)
	}
	return nil
}

func spoolFree(dir string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
