package domain

import (
	"regexp"
	"strings"
)

// FailureKind is the stable, plain reason a provisioning failed. It is what a
// customer reads in place of the raw failure text, which names pods, volumes
// and namespaces (EXC-869). The kinds match the Studio's wording map in
// frontend/src/utils/provisionFailure.ts.
type FailureKind string

const (
	FailureOutOfMemory FailureKind = "out_of_memory"
	FailureStorage     FailureKind = "storage"
	FailureImage       FailureKind = "image"
	FailureCrashing    FailureKind = "crashing"
	FailureNoCapacity  FailureKind = "no_capacity"
	FailureTimeout     FailureKind = "timeout"
	FailureUnknown     FailureKind = "unknown"
)

// failureRules are ordered: the first match wins, so the specific causes come
// before the broad ones. Keep them in step with the Studio's RULES.
var failureRules = []struct {
	pattern *regexp.Regexp
	kind    FailureKind
}{
	{regexp.MustCompile(`(?i)OOMKilled|out of memory`), FailureOutOfMemory},
	{regexp.MustCompile(`(?i)persistentvolumeclaim|\bPVC\b|FailedAttachVolume|FailedMount|Multi-Attach`), FailureStorage},
	{regexp.MustCompile(`(?i)ImagePullBackOff|ErrImagePull|InvalidImageName`), FailureImage},
	{regexp.MustCompile(`(?i)CrashLoopBackOff`), FailureCrashing},
	{regexp.MustCompile(`(?i)Unschedulable|Insufficient (?:cpu|memory)|nodes? are available`), FailureNoCapacity},
	// Anchored to start-up wording, so a database's own "lock timeout" stays unknown.
	{regexp.MustCompile(`(?i)did not become ready|timed out waiting for|(?:provision\w*|readiness)\W.*?(?:timed out|timeout|deadline exceeded)`), FailureTimeout},
}

// ClassifyFailure maps raw failure text to a kind. Empty text has no kind;
// text no rule recognises is unknown, never passed through.
func ClassifyFailure(raw string) FailureKind {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	for _, rule := range failureRules {
		if rule.pattern.MatchString(text) {
			return rule.kind
		}
	}
	return FailureUnknown
}
