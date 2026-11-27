// Package planbudget holds the CPU, memory and disk an organization may reserve
// across its services, and decides whether one more reservation fits.
package planbudget

import (
	"fmt"
	"strconv"
	"strings"
)

// Size is a reserved amount: what a service is sized to, or what an org may hold.
type Size struct {
	CPUMilli    int64 `json:"cpuMilli"`
	MemoryBytes int64 `json:"memoryBytes"`
	DiskBytes   int64 `json:"diskBytes"`
	// Services counts databases, apps and a functions runtime; a database with
	// several copies is one service.
	Services int64 `json:"services"`
}

func (s Size) Add(o Size) Size {
	return Size{s.CPUMilli + o.CPUMilli, s.MemoryBytes + o.MemoryBytes, s.DiskBytes + o.DiskBytes, s.Services + o.Services}
}

func (s Size) Sub(o Size) Size {
	return Size{s.CPUMilli - o.CPUMilli, s.MemoryBytes - o.MemoryBytes, s.DiskBytes - o.DiskBytes, s.Services - o.Services}
}

// Times scales a per-copy size by the number of copies or replicas. The service
// count does not scale: copies of one database are one service.
func (s Size) Times(n int64) Size {
	return Size{s.CPUMilli * n, s.MemoryBytes * n, s.DiskBytes * n, s.Services}
}

func (s Size) IsZero() bool { return s == Size{} }

// Refusal is the answer when a growth step does not fit. Every number is the
// user's own: never server capacity.
type Refusal struct {
	Dimension string
	Need      Size
	Left      Size
	Budget    Size
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("plan budget exceeded: this needs %s more %s but only %s is left (your plan allows %s)",
		r.amount(r.Need), r.Dimension, r.amount(r.Left), r.amount(r.Budget))
}

func (r *Refusal) amount(s Size) string {
	switch r.Dimension {
	case "CPU":
		return fmt.Sprintf("%.2f vCPU", float64(s.CPUMilli)/1000)
	case "memory":
		return formatBytes(s.MemoryBytes)
	case "services":
		return fmt.Sprintf("%d", s.Services)
	default:
		return formatBytes(s.DiskBytes)
	}
}

func formatBytes(n int64) string {
	const mib = int64(1) << 20
	const gib = int64(1) << 30
	if n >= gib && n%gib == 0 {
		return fmt.Sprintf("%d GiB", n/gib)
	}
	return fmt.Sprintf("%d MiB", n/mib)
}

// Decide says whether delta fits under ceiling on top of used. A dimension that
// does not grow is never refused, so shrinking, deleting and pausing pass even
// for an org already over its budget.
func Decide(used, delta, ceiling Size) error {
	for _, d := range []dimension{cpuDim, memoryDim, diskDim, servicesDim} {
		add := d.get(delta)
		if add <= 0 {
			continue
		}
		left := d.get(ceiling) - d.get(used)
		if add > left {
			return &Refusal{Dimension: d.name, Need: d.only(add), Left: d.only(max(left, 0)), Budget: ceiling}
		}
	}
	return nil
}

type dimension struct {
	name string
	get  func(Size) int64
	only func(int64) Size
}

var (
	cpuDim      = dimension{"CPU", func(s Size) int64 { return s.CPUMilli }, func(n int64) Size { return Size{CPUMilli: n} }}
	memoryDim   = dimension{"memory", func(s Size) int64 { return s.MemoryBytes }, func(n int64) Size { return Size{MemoryBytes: n} }}
	servicesDim = dimension{"services", func(s Size) int64 { return s.Services }, func(n int64) Size { return Size{Services: n} }}
	diskDim     = dimension{"disk", func(s Size) int64 { return s.DiskBytes }, func(n int64) Size { return Size{DiskBytes: n} }}
)

// ParseCPUMilli reads a Kubernetes-style CPU quantity: "0.5", "2" or "250m".
func ParseCPUMilli(text string) (int64, error) {
	text = strings.TrimSpace(text)
	if strings.HasSuffix(text, "m") {
		n, err := strconv.ParseInt(strings.TrimSuffix(text, "m"), 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("cpu %q is not a quantity", text)
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("cpu %q is not a quantity", text)
	}
	return int64(f*1000 + 0.5), nil
}

var byteSuffixes = []struct {
	suffix string
	factor int64
}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}}

// ParseBytes reads a Kubernetes-style byte quantity: "512Mi", "5Gi" or plain bytes.
func ParseBytes(text string) (int64, error) {
	text = strings.TrimSpace(text)
	for _, s := range byteSuffixes {
		if strings.HasSuffix(text, s.suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(text, s.suffix), 10, 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("size %q is not a quantity", text)
			}
			return n * s.factor, nil
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("size %q is not a quantity", text)
	}
	return n, nil
}
