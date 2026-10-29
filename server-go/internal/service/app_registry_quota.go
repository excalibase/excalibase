package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/appregistry"
)

// RegistryQuotaImages is a project's room in the registry, in its plan's largest image:
// enough for a new build beside what runs, shared by the project's apps.
const RegistryQuotaImages = 3

// ErrRegistryFull fails a build whose image does not fit beside the images the project runs.
var ErrRegistryFull = errors.New("this project's built images fill its registry space")

// pushedImage is one push tag and what its image holds.
type pushedImage struct {
	repository, tag, digest string
	at                      time.Time
	blobs                   map[string]int64
}

// MakeRoom fits incoming (an image's blobs, by digest) into the project's
// quota: the oldest images no app runs lose their tags until it fits.
// Shared layers count once. An image an app runs is never taken.
func (s *RegistrySweeper) MakeRoom(ctx context.Context, projectID string, incoming map[string]int64, quota int64) error {
	images, running, err := s.projectImages(ctx, projectID)
	if err != nil {
		return err
	}
	sort.Slice(images, func(i, j int) bool { return images[i].at.Before(images[j].at) })
	for {
		used, need := usage(images, incoming)
		if used+need <= quota {
			return nil
		}
		victim := -1
		for i, image := range images {
			if !running[image.repository][image.digest] {
				victim = i
				break
			}
		}
		if victim < 0 {
			return fmt.Errorf("%w: %d bytes in use and %d more needed, of %d", ErrRegistryFull, used, need, quota)
		}
		if err := s.registry.Untag(ctx, images[victim].repository, images[victim].tag); err != nil {
			return fmt.Errorf("make room in the registry: %w", err)
		}
		images = append(images[:victim:victim], images[victim+1:]...)
	}
}

// projectImages lists every push tag in the project's repositories, and the digests each app runs.
func (s *RegistrySweeper) projectImages(ctx context.Context, projectID string) ([]pushedImage, map[string]map[string]bool, error) {
	repositories, err := s.registry.Repositories(ctx)
	if err != nil {
		return nil, nil, err
	}
	var images []pushedImage
	running := map[string]map[string]bool{}
	for _, repository := range repositories {
		if !strings.HasPrefix(repository, projectID+"/") || !appregistry.OwnedBy(repository, projectID) {
			continue
		}
		if running[repository], err = s.keepRecent(ctx, repository, 0); err != nil {
			return nil, nil, err
		}
		found, err := s.repositoryImages(ctx, repository)
		if err != nil {
			return nil, nil, err
		}
		images = append(images, found...)
	}
	return images, running, nil
}

func (s *RegistrySweeper) repositoryImages(ctx context.Context, repository string) ([]pushedImage, error) {
	tags, err := s.registry.Tags(ctx, repository)
	if err != nil {
		return nil, err
	}
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
		images = append(images, pushedImage{repository: repository, tag: tag, digest: digest, at: at, blobs: read[digest]})
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
