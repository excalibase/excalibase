package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
)

// RegistryQuotaImages is a project's room in the registry, in its plan's largest image:
// enough for a new build beside what runs, shared by the project's apps.
// The bound is soft: it counts the blobs tagged images hold, so blobs left
// untagged but not yet freed by zot's GC are not counted.
const RegistryQuotaImages = 3

// registryQuotaDeployWindow is how many of an app's newest deploys the quota
// reads for the one in flight (a new deploy supersedes any other) and for the
// pushes already recorded; a push recorded further back stays under the grace.
const registryQuotaDeployWindow = 20

// ErrRegistryFull fails a build whose image does not fit beside the images the project keeps.
var ErrRegistryFull = errors.New("this project's built images fill its registry space")

// RegistryFullError is ErrRegistryFull with the numbers: the bytes the kept
// images use, what the new image adds to them, and the quota.
type RegistryFullError struct {
	Used, Incoming, Quota int64
}

func (e *RegistryFullError) Error() string {
	return fmt.Sprintf("%v: %d bytes kept and %d more needed, of %d", ErrRegistryFull, e.Used, e.Incoming, e.Quota)
}

func (e *RegistryFullError) Unwrap() error { return ErrRegistryFull }

// pushedImage is one push tag and what its image holds. removable is a tag
// the quota may take: not run, not in a deploy in flight, not the new image,
// and either recorded by a deploy or stale by the sweep's grace (staleTags).
type pushedImage struct {
	repository, tag, digest string
	at                      time.Time
	blobs                   map[string]int64
	removable               bool
}

// Fits says whether incoming (an image's blobs, by digest) fits the quota
// beside the images MakeRoom may not take. It removes nothing, so a build that
// cannot fit fails with every rollback image in place. Shared layers count once.
func (s *RegistrySweeper) Fits(ctx context.Context, projectID string, incoming map[string]int64, quota int64) error {
	images, err := s.projectImages(ctx, projectID, "")
	if err != nil {
		return err
	}
	used, need := usage(kept(images), incoming)
	if used+need > quota {
		return &RegistryFullError{Used: used, Incoming: need, Quota: quota}
	}
	return nil
}

// MakeRoom brings the project back within its quota after the image pushed
// has been pushed: the oldest removable images lose their tags until it fits.
// It runs after the push, under the publish lock, so a push that fails leaves
// every older tag; the pushed image is never taken.
func (s *RegistrySweeper) MakeRoom(ctx context.Context, projectID, pushed string, quota int64) error {
	images, err := s.projectImages(ctx, projectID, pushed)
	if err != nil {
		return err
	}
	sort.Slice(images, func(i, j int) bool { return images[i].at.Before(images[j].at) })
	for {
		used, _ := usage(images, nil)
		if used <= quota {
			return nil
		}
		victim := slices.IndexFunc(images, func(image pushedImage) bool { return image.removable })
		if victim < 0 {
			return &RegistryFullError{Used: used, Quota: quota}
		}
		if err := s.registry.Untag(ctx, images[victim].repository, images[victim].tag); err != nil {
			return fmt.Errorf("make room in the registry: %w", err)
		}
		images = slices.Delete(images, victim, victim+1)
	}
}

func kept(images []pushedImage) []pushedImage {
	return slices.DeleteFunc(slices.Clone(images), func(image pushedImage) bool { return image.removable })
}

// projectImages lists every push tag in the project's repositories, each marked removable or not.
func (s *RegistrySweeper) projectImages(ctx context.Context, projectID, pushed string) ([]pushedImage, error) {
	repositories, err := s.registry.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	var images []pushedImage
	for _, repository := range repositories {
		if !strings.HasPrefix(repository, projectID+"/") || !appregistry.OwnedBy(repository, projectID) {
			continue
		}
		keep, err := s.quotaKeep(ctx, repository, pushed)
		if err != nil {
			return nil, err
		}
		found, err := s.repositoryImages(ctx, repository, keep)
		if err != nil {
			return nil, err
		}
		images = append(images, found...)
	}
	return images, nil
}

// quotaKeep is what the quota never takes from a repository, a narrower set
// than the sweep's: the image the app names, the one it runs, its deploys in
// flight and the image just pushed. Older rollback images are not kept here.
// recorded is every image a recent deploy names: the grace only spares a push
// whose deploy is not recorded yet.
func (s *RegistrySweeper) quotaKeep(ctx context.Context, repository, pushed string) (quotaKeepSet, error) {
	keep := quotaKeepSet{keep: map[string]bool{}, recorded: map[string]bool{}}
	keepDigest(keep.keep, "@"+pushed)
	app, err := s.repositoryApp(ctx, repository)
	if err != nil || app == nil {
		return keep, err
	}
	keepDigest(keep.keep, app.Image)
	running, err := s.deploys.GetLastSucceeded(app.ProjectID, app.ID)
	if err != nil {
		return keep, err
	}
	if running != nil {
		keepDeployImage(keep.keep, running)
	}
	recent, err := s.deploys.ListByApp(app.ProjectID, app.ID, registryQuotaDeployWindow)
	if err != nil {
		return keep, err
	}
	for _, deploy := range recent {
		keepDeployImage(keep.recorded, deploy)
		if inFlight(deploy) {
			keepDeployImage(keep.keep, deploy)
		}
	}
	return keep, nil
}

// quotaKeepSet is what the quota keeps of a repository and which images a deploy has recorded.
type quotaKeepSet struct {
	keep, recorded map[string]bool
}

// removable is the push tags the quota may take: recorded ones it does not
// keep, and the rest of what the sweep's grace does not spare. A recorded
// push leaves the grace's ranking, so it never holds a slot an unrecorded one needs.
func (s *RegistrySweeper) removable(tags []string, keep quotaKeepSet) map[string]bool {
	spared := maps.Clone(keep.keep)
	maps.Copy(spared, keep.recorded)
	removable := map[string]bool{}
	for _, tag := range tags {
		if digest, _, ok := appregistry.ParsePushTag(tag); ok && keep.recorded[digest] && !keep.keep[digest] {
			removable[tag] = true
		}
	}
	for _, tag := range s.staleTags(tags, spared) {
		removable[tag] = true
	}
	return removable
}

func inFlight(deploy *apphost.Deploy) bool {
	return deploy.Status == apphost.DeployStatusPending || deploy.Status == apphost.DeployStatusRolling
}

func (s *RegistrySweeper) repositoryImages(ctx context.Context, repository string, keep quotaKeepSet) ([]pushedImage, error) {
	tags, err := s.registry.Tags(ctx, repository)
	if err != nil {
		return nil, err
	}
	stale := s.removable(tags, keep)
	var images []pushedImage
	read := map[string]map[string]int64{}
	for _, tag := range tags {
		digest, at, ok := appregistry.ParsePushTag(tag)
		if !ok {
			continue
		}
		if read[digest] == nil {
			if read[digest], err = s.registry.Blobs(ctx, repository, tag); err != nil {
				return nil, err
			}
		}
		images = append(images, pushedImage{repository: repository, tag: tag, digest: digest, at: at, blobs: read[digest], removable: stale[tag]})
	}
	return images, nil
}

// usage is the bytes the images hold, and what incoming adds that they do not.
func usage(images []pushedImage, incoming map[string]int64) (int64, int64) {
	held := map[string]int64{}
	for _, image := range images {
		for digest, size := range image.blobs {
			held[digest] = size
		}
	}
	var used, need int64
	for _, size := range held {
		used += size
	}
	for digest, size := range incoming {
		if _, ok := held[digest]; !ok {
			need += size
		}
	}
	return used, need
}
