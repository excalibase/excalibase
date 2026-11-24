package domain

import "testing"

func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		text string
		want FailureKind
	}{
		{"OOMKilled", FailureOutOfMemory},
		{"container ran out of memory", FailureOutOfMemory},
		{"Out Of Memory", FailureOutOfMemory},
		{`persistentvolumeclaim "data-1" not found`, FailureStorage},
		{"PVC data-1 is pending", FailureStorage},
		{"FailedAttachVolume for data-1", FailureStorage},
		{"FailedMount for data-1", FailureStorage},
		{"Multi-Attach error", FailureStorage},
		{"ImagePullBackOff", FailureImage},
		{"ErrImagePull", FailureImage},
		{"InvalidImageName", FailureImage},
		{"pod p1-postgres-1 in CrashLoopBackOff", FailureCrashing},
		{"Unschedulable", FailureNoCapacity},
		{"Insufficient cpu", FailureNoCapacity},
		{"Insufficient memory", FailureNoCapacity},
		{"0/3 nodes are available", FailureNoCapacity},
		{"the database did not become ready", FailureTimeout},
		{"timed out waiting for the database", FailureTimeout},
		{"provisioning timed out", FailureTimeout},
		{"provisioning timeout", FailureTimeout},
		{"readiness deadline exceeded", FailureTimeout},
		// the first matching rule wins, specific before broad
		{"OOMKilled then CrashLoopBackOff", FailureOutOfMemory},
		// a timeout that is not about starting up stays unknown
		{"canceling statement due to lock timeout", FailureUnknown},
		{"etcdserver: request timed out", FailureUnknown},
		{"context deadline exceeded", FailureUnknown},
		// unknown text is never passed through
		{"something nobody anticipated in ns org1-p1", FailureUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyFailure(tc.text); got != tc.want {
			t.Errorf("ClassifyFailure(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestClassifyFailureOfNothingIsNoKind(t *testing.T) {
	for _, text := range []string{"", "  \n"} {
		if got := ClassifyFailure(text); got != "" {
			t.Errorf("ClassifyFailure(%q) = %q, want no kind", text, got)
		}
	}
}
