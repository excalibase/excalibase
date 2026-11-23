package config

import "os"

// defaultCrashLoopMaxPerHour is how many crashes of one app in an hour stop it.
const defaultCrashLoopMaxPerHour = 10

// defaultCrashGuardExempt is the project the guard never touches unless an install says otherwise.
const defaultCrashGuardExempt = "proj-jfp7kx46kb"

// CrashLoopConfig tunes the crash-loop guard (EXC-839).
type CrashLoopConfig struct {
	// MaxPerHour is the crash count, over a rolling hour, at which an app is stopped.
	MaxPerHour int
	// ExemptProjects are never stopped or alerted on.
	ExemptProjects []string
}

// loadCrashLoop reads APP_CRASH_LOOP_MAX_PER_HOUR (a value below 1 keeps the
// default) and CRASH_GUARD_EXEMPT_PROJECTS (unset keeps the default; "none" empties it).
func loadCrashLoop() CrashLoopConfig {
	max := envInt("APP_CRASH_LOOP_MAX_PER_HOUR", defaultCrashLoopMaxPerHour)
	if max < 1 {
		max = defaultCrashLoopMaxPerHour
	}
	exempt := []string{defaultCrashGuardExempt}
	if raw := os.Getenv("CRASH_GUARD_EXEMPT_PROJECTS"); raw == "none" {
		exempt = nil
	} else if raw != "" {
		exempt = envList("CRASH_GUARD_EXEMPT_PROJECTS")
	}
	return CrashLoopConfig{MaxPerHour: max, ExemptProjects: exempt}
}
