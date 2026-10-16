// Package fenceprobe is the start-up probe the network-fence tests run as the
// first thing a workload does (EXC-622): it dials every target in parallel,
// round after round, and logs whether each one answered, so a test can tell
// whether anything the fence denies was reachable at any moment after start.
package fenceprobe

import (
	"bufio"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	// Marker starts every line the probe logs.
	Marker = "FENCEPROBE"
	// Open and Blocked are a target's state in one round.
	Open    = "OPEN"
	Blocked = "BLOCKED"
	// SecondsEnv and TargetsEnv configure the script: how long to probe, and
	// the targets as space-separated name=host:port pairs.
	SecondsEnv = "PROBE_SECONDS"
	TargetsEnv = "PROBE_TARGETS"
	// Port is where the script serves HTTP once it has finished probing.
	Port = 8080
)

// Script is a one-line busybox sh script (app arguments may not hold a
// newline). Each round logs "FENCEPROBE <seconds since the script started>
// <target> OPEN|BLOCKED"; it ends with "FENCEPROBE end" and then serves HTTP on Port.
const Script = `t0=$(cut -d' ' -f1 /proc/uptime); ` +
	`elapsed() { awk -v start="$t0" '{printf "%.2f", $1 - start}' /proc/uptime; }; ` +
	`echo "FENCEPROBE begin epoch=$(date +%s)"; ` +
	`while awk -v now="$(elapsed)" -v stop="$PROBE_SECONDS" 'BEGIN{exit !(now < stop)}'; do ` +
	`at=$(elapsed); for target in $PROBE_TARGETS; do ( name=${target%%=*}; address=${target#*=}; ` +
	`if nc -z -w 1 "${address%:*}" "${address##*:}" </dev/null >/dev/null 2>&1; then state=OPEN; else state=BLOCKED; fi; ` +
	`echo "FENCEPROBE $at $name $state" ) & done; wait; usleep 200000; done; ` +
	`echo "FENCEPROBE end"; exec httpd -f -p 8080 -h /`

// EndLine is the line the script logs after its last round.
const EndLine = Marker + " end"

// Target is one address the probe dials; a fenced one must never answer.
type Target struct {
	Name, Addr string
	Fenced     bool
}

// Sample is one target's state in one round.
type Sample struct {
	At    float64
	Name  string
	State string
}

// TargetsValue renders targets as the script's TargetsEnv value.
func TargetsValue(targets []Target) string {
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		parts = append(parts, target.Name+"="+target.Addr)
	}
	return strings.Join(parts, " ")
}

// Parse reads the probe's samples out of a log, ignoring every other line.
func Parse(logs string) ([]Sample, error) {
	var samples []Sample
	scanner := bufio.NewScanner(strings.NewReader(logs))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 4 || fields[0] != Marker {
			continue
		}
		at, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return nil, fmt.Errorf("probe line %q: %w", scanner.Text(), err)
		}
		if fields[3] != Open && fields[3] != Blocked {
			return nil, fmt.Errorf("probe line %q: unknown state", scanner.Text())
		}
		samples = append(samples, Sample{At: at, Name: fields[2], State: fields[3]})
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("no probe samples in %q", logs)
	}
	return samples, nil
}

// FirstOpen is when name first answered at or after from; -1 when it never did.
func FirstOpen(samples []Sample, name string, from float64) float64 {
	first := -1.0
	for _, sample := range samples {
		if sample.Name == name && sample.State == Open && sample.At >= from && (first < 0 || sample.At < first) {
			first = sample.At
		}
	}
	return first
}

// Breaches names each fenced target that answered at or after from, with the first time it did.
func Breaches(samples []Sample, targets []Target, from float64) []string {
	var breaches []string
	for _, target := range targets {
		if !target.Fenced {
			continue
		}
		if at := FirstOpen(samples, target.Name, from); at >= 0 {
			breaches = append(breaches, fmt.Sprintf("%s (%s) answered at t=%.2fs", target.Name, target.Addr, at))
		}
	}
	return breaches
}

// Timeline is one line per round: its time and every target's state.
func Timeline(samples []Sample) string {
	rounds := map[float64][]string{}
	var times []float64
	for _, sample := range samples {
		if _, seen := rounds[sample.At]; !seen {
			times = append(times, sample.At)
		}
		rounds[sample.At] = append(rounds[sample.At], sample.Name+"="+sample.State)
	}
	sort.Float64s(times)
	var out strings.Builder
	for _, at := range times {
		sort.Strings(rounds[at])
		fmt.Fprintf(&out, "  t=%5.2fs %s\n", at, strings.Join(rounds[at], " "))
	}
	return out.String()
}
