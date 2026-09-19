package storagesvc

import (
	"context"
	"fmt"
	"strings"
)

// projectPurgePageSize bounds one listing of a project's objects.
const projectPurgePageSize = 1000

// projectPrefix is the key namespace holding every object of one project,
// across all of its buckets:
//
//	projects/<projectId>/
//
// The trailing slash is what keeps one project from reaching another whose id
// merely starts with the same characters — "proj-x" must never match the keys
// of "proj-xtra". Ids that could climb out of the namespace are refused here
// rather than sanitised, because there is no safe interpretation of them.
func projectPrefix(projectID string) (string, error) {
	if strings.TrimSpace(projectID) == "" ||
		strings.ContainsAny(projectID, "/\\") ||
		strings.Contains(projectID, "..") {
		return "", invalidf("unsafe project id %q", projectID)
	}
	return fmt.Sprintf("projects/%s/", projectID), nil
}

// PurgeProjectObjects deletes every object stored under a project's prefix
// and reports how many went.
//
// A deleted project's catalogue rows go with its record, and after that
// nothing names these bytes: there is no bucket to list them through, no
// project to scope a request to, and no row to bill from. They would sit in
// the bucket forever. Everything under the prefix belongs to this project by
// construction, so the purge does not need — and does not have — a catalogue
// to work from: it clears the namespace itself, which also takes the uploads
// that were never confirmed.
//
// Idempotent: a project whose prefix is already clear purges nothing and
// succeeds, so a retried deletion resumes instead of failing forever. The
// prefix is listed once more at the end, because "the deletes returned no
// error" is not the same as "the prefix is empty".
func (s *Service) PurgeProjectObjects(ctx context.Context, projectID string) (int, error) {
	prefix, err := projectPrefix(projectID)
	if err != nil {
		return 0, err
	}
	if s.objects == nil {
		return 0, errObjectStoreUnset
	}

	deleted := 0
	previousHead := ""
	for {
		keys, err := s.objects.ListKeysWithPrefix(ctx, prefix, projectPurgePageSize)
		if err != nil {
			return deleted, fmt.Errorf("list project objects: %w", err)
		}
		if len(keys) == 0 {
			return deleted, nil
		}
		// A page that comes back identical after its keys were deleted means
		// the store accepted deletes that did nothing. Stop and say so rather
		// than spinning on it.
		if keys[0] == previousHead {
			return deleted, fmt.Errorf("purge did not converge: %q is still stored", keys[0])
		}
		previousHead = keys[0]
		for _, key := range keys {
			if err := s.objects.DeleteKey(ctx, prefix, key); err != nil {
				return deleted, fmt.Errorf("delete project object: %w", err)
			}
			deleted++
		}
	}
}
