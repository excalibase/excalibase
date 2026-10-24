package requestmetrics

import (
	"math"
	"testing"
)

func TestQuantile_InterpolatesInsideTheBucket(t *testing.T) {
	buckets := make([]int64, BucketCount)
	buckets[0] = 50 // <= 5ms
	buckets[1] = 50 // 5-10ms
	if got, ok := QuantileMs(buckets, 0.5); !ok || math.Abs(got-5) > 1e-9 {
		t.Errorf("p50: %v", got)
	}
	if got, _ := QuantileMs(buckets, 0.95); math.Abs(got-9.5) > 1e-9 {
		t.Errorf("p95: %v", got)
	}
}

func TestQuantile_AboveTheLastBoundIsTheLastBound(t *testing.T) {
	buckets := make([]int64, BucketCount)
	buckets[BucketCount-1] = 10
	if got, _ := QuantileMs(buckets, 0.95); got != 10000 {
		t.Errorf("p95 past 10s: %v", got)
	}
}

func TestQuantile_NoRequestsHasNoLatency(t *testing.T) {
	if _, ok := QuantileMs(make([]int64, BucketCount), 0.5); ok {
		t.Error("no requests, no percentile")
	}
}

func TestMerge_AddsBucketsSoPercentilesSpanPodsAndServices(t *testing.T) {
	fast := make([]int64, BucketCount)
	fast[0] = 90
	slow := make([]int64, BucketCount)
	slow[6] = 10 // 250-500ms
	merged := Usage{}.Merge(Usage{Requests: 90, Buckets: fast}).Merge(Usage{Requests: 10, Buckets: slow, ServerErrors: 1})
	if merged.Requests != 100 || merged.ServerErrors != 1 {
		t.Errorf("merge: %+v", merged)
	}
	if got, _ := QuantileMs(merged.Buckets, 0.95); got <= 250 || got > 500 {
		t.Errorf("p95 of the merged histogram falls in the slow bucket: %v", got)
	}
}
