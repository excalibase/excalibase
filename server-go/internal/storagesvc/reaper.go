package storagesvc

import (
	"context"
	"fmt"
	"time"
)

// DefaultUnconfirmedGrace is how long an object with no catalogue row is left
// alone before it is treated as an abandoned upload. It has to exceed the
// signed URL's lifetime plus the round trip a client needs to confirm; an
// hour is generous on both counts.
const DefaultUnconfirmedGrace = time.Hour

// reaperPageSize bounds one listing of a bucket's objects.
const reaperPageSize = 1000

// confirmSafetyMargin is how much earlier than the reaper's grace a
// confirmation stops being accepted. The two windows must not touch: a
// confirm admitted at the same instant the reaper decided an object was
// abandoned would record a row for bytes about to be deleted. The margin is
// the signed URL's own lifetime, which bounds how long a PUT can still be in
// flight when the window closes.
const confirmSafetyMargin = uploadURLTTL

// confirmWindow is how old a stored object may be and still be confirmable.
func confirmWindow(grace time.Duration) time.Duration {
	if grace <= confirmSafetyMargin {
		return grace / 2
	}
	return grace - confirmSafetyMargin
}

// ReapReport says what one sweep did. Deleted holds "<bucket>/<key>" for each
// abandoned object removed; Failed names the buckets that could not be swept.
type ReapReport struct {
	Deleted []string
	Failed  []string
}

// ReapUnconfirmedUploads deletes objects that reached the blob plane but were
// never confirmed. Without it those bytes are invisible to quota accounting —
// nothing records them, so nothing charges for them — and a caller could take
// an upload URL, PUT to it and never confirm, over and over, without ever
// hitting a limit. Anything younger than grace is left alone: it may be an
// upload still in flight.
//
// A per-bucket failure is collected rather than fatal, so one unreachable
// bucket does not stop the sweep.
func (s *Service) ReapUnconfirmedUploads(ctx context.Context, grace time.Duration, now time.Time) (ReapReport, error) {
	var report ReapReport
	if s.objects == nil {
		return report, fmt.Errorf("object store not configured")
	}
	if grace <= 0 {
		grace = DefaultUnconfirmedGrace
	}
	buckets, err := s.store.ListAllBuckets(ctx)
	if err != nil {
		return report, fmt.Errorf("list buckets: %w", err)
	}
	cutoff := now.Add(-grace)
	for _, bucket := range buckets {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		// A bucket mid-delete belongs to the delete path, which purges every
		// object under its prefix regardless of age. Sweeping it here would
		// race that purge over the same keys for no gain.
		if bucket.Status == BucketStatusDeleting {
			continue
		}
		if err := s.reapBucket(ctx, bucket, cutoff, &report); err != nil {
			report.Failed = append(report.Failed, bucket.ProjectID+"/"+bucket.Name)
		}
	}
	return report, nil
}

// reapBucket removes every object of one bucket that is older than cutoff and
// has no catalogue row.
func (s *Service) reapBucket(ctx context.Context, bucket Bucket, cutoff time.Time, report *ReapReport) error {
	stored, err := s.objects.ListObjects(ctx, bucket.ProjectID, bucket.ID, reaperPageSize)
	if err != nil {
		return err
	}
	for _, obj := range stored {
		if !obj.LastModified.Before(cutoff) {
			continue
		}
		// Re-read the row immediately before deleting. A confirmation cannot
		// land in this gap — it is refused once an object is older than the
		// confirm window, which closes before this cutoff — but reading late
		// keeps the gap as small as it can be.
		row, err := s.store.GetObject(ctx, bucket.ID, obj.Key)
		if err != nil {
			return fmt.Errorf("look up object %q: %w", obj.Key, err)
		}
		if row != nil {
			continue
		}
		if err := s.objects.DeleteObject(ctx, bucket.ProjectID, bucket.ID, obj.Key); err != nil {
			return fmt.Errorf("delete abandoned object %q: %w", obj.Key, err)
		}
		report.Deleted = append(report.Deleted, bucket.Name+"/"+obj.Key)
	}
	return nil
}
