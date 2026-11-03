// Package hostmetrics reads a single host's CPU, memory, load, disk, disk
// I/O, network and temperatures from inside an unprivileged container
// (EXC-670). procfs and sysfs already describe the host for all of these but
// the network, whose interfaces live in the host's network namespace: compose
// bind-mounts the host's /proc/1/net/dev read-only for that one file.
package hostmetrics

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// sectorBytes is the unit of /proc/diskstats, whatever the disk's own sector size.
const sectorBytes = 512

// Paths are where the host's files are mounted; NetDev may be empty.
type Paths struct {
	Proc   string
	Sys    string
	NetDev string
	// Disk is a path on the filesystem whose use is reported: the engine's storage.
	Disk string
}

// DefaultPaths are the locations inside the provisioning container.
func DefaultPaths(netDev string) Paths {
	return Paths{Proc: "/proc", Sys: "/sys", NetDev: netDev, Disk: "/"}
}

// Sensor is one temperature reading.
type Sensor struct {
	Sensor  string  `json:"sensor"`
	Celsius float64 `json:"celsius"`
}

// Counters is one reading of the host's cumulative counters and gauges.
type Counters struct {
	At                            time.Time
	CPUTotal, CPUIdle             uint64
	CPUCount                      int
	LoadOK                        bool
	Load1, Load5, Load15          float64
	MemOK                         bool
	MemTotal, MemAvailable        uint64
	SwapTotal, SwapFree           uint64
	DiskOK                        bool
	DiskTotal, DiskUsed           uint64
	DiskIOOK                      bool
	DiskReadBytes, DiskWriteBytes uint64
	NetOK                         bool
	NetRx, NetTx                  uint64
	Temperatures                  []Sensor
}

// Read takes one reading; problems name what could not be read, and the
// figures they cover stay unset.
func Read(paths Paths, now time.Time) (Counters, []error) {
	reading := Counters{At: now}
	var problems []error
	note := func(err error) {
		if err != nil {
			problems = append(problems, err)
		}
	}
	note(readCPU(paths.Proc, &reading))
	note(readMemory(paths.Proc, &reading))
	note(readLoad(paths.Proc, &reading))
	note(readDisk(paths.Disk, &reading))
	note(readDiskIO(paths.Proc, paths.Sys, &reading))
	if paths.NetDev != "" {
		note(readNetwork(paths.NetDev, &reading))
	}
	reading.Temperatures = readTemperatures(paths.Sys)
	return reading, problems
}

func readCPU(proc string, reading *Counters) error {
	raw, err := os.ReadFile(filepath.Join(proc, "stat"))
	if err != nil {
		return fmt.Errorf("cpu: %w", err)
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			reading.CPUCount++
			continue
		}
		// user nice system idle iowait irq softirq steal; guest is inside user.
		for i, field := range fields[1:min(len(fields), 9)] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return fmt.Errorf("cpu: %q: %w", field, err)
			}
			reading.CPUTotal += value
			if i == 3 || i == 4 {
				reading.CPUIdle += value
			}
		}
		found = true
	}
	if !found {
		reading.CPUCount = 0
		return errors.New("cpu: no aggregate line in stat")
	}
	return nil
}

func readMemory(proc string, reading *Counters) error {
	raw, err := os.ReadFile(filepath.Join(proc, "meminfo"))
	if err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		fields := strings.Fields(rest)
		if !ok || len(fields) == 0 {
			continue
		}
		if value, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			values[name] = value * 1024
		}
	}
	total, hasTotal := values["MemTotal"]
	available, hasAvailable := values["MemAvailable"]
	if !hasTotal || !hasAvailable || total == 0 {
		return errors.New("memory: MemTotal or MemAvailable missing")
	}
	reading.MemOK = true
	reading.MemTotal, reading.MemAvailable = total, available
	reading.SwapTotal, reading.SwapFree = values["SwapTotal"], values["SwapFree"]
	return nil
}

func readLoad(proc string, reading *Counters) error {
	raw, err := os.ReadFile(filepath.Join(proc, "loadavg"))
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return errors.New("load: loadavg is short")
	}
	var loads [3]float64
	for i := range loads {
		if loads[i], err = strconv.ParseFloat(fields[i], 64); err != nil {
			return fmt.Errorf("load: %w", err)
		}
	}
	reading.LoadOK = true
	reading.Load1, reading.Load5, reading.Load15 = loads[0], loads[1], loads[2]
	return nil
}

func readDisk(path string, reading *Counters) error {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return fmt.Errorf("disk: %w", err)
	}
	blockSize := uint64(fs.Bsize)
	used := (fs.Blocks - fs.Bfree) * blockSize
	// As df: what is used plus what an unprivileged writer may still take.
	total := used + fs.Bavail*blockSize
	if total == 0 {
		return errors.New("disk: the filesystem reports no size")
	}
	reading.DiskOK = true
	reading.DiskTotal, reading.DiskUsed = total, used
	return nil
}

// readDiskIO sums whole physical disks: a disk with a device behind it in
// /sys/block, so loop, device-mapper and partitions are never counted twice.
func readDiskIO(proc, sys string, reading *Counters) error {
	raw, err := os.ReadFile(filepath.Join(proc, "diskstats"))
	if err != nil {
		return fmt.Errorf("disk io: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || !physicalDisk(sys, fields[2]) {
			continue
		}
		read, readErr := strconv.ParseUint(fields[5], 10, 64)
		written, writeErr := strconv.ParseUint(fields[9], 10, 64)
		if readErr != nil || writeErr != nil {
			continue
		}
		reading.DiskIOOK = true
		reading.DiskReadBytes += read * sectorBytes
		reading.DiskWriteBytes += written * sectorBytes
	}
	return nil
}

func physicalDisk(sys, name string) bool {
	if strings.ContainsAny(name, "/.") {
		return false
	}
	_, err := os.Stat(filepath.Join(sys, "block", name, "device"))
	return err == nil
}

// virtualInterfaces are the engine's and the kernel's own links; their
// traffic is the host's traffic seen a second time.
var virtualInterfaces = []string{"lo", "veth", "docker", "br-", "virbr", "podman", "cni", "vnet", "flannel", "cali", "kube", "tunl", "vxlan", "pasta", "tap"}

func readNetwork(path string, reading *Counters) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("network: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		name, rest, ok := strings.Cut(scanner.Text(), ":")
		name = strings.TrimSpace(name)
		fields := strings.Fields(rest)
		if !ok || len(fields) < 9 || isVirtual(name) {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr != nil || txErr != nil {
			continue
		}
		reading.NetOK = true
		reading.NetRx += rx
		reading.NetTx += tx
	}
	if !reading.NetOK {
		return errors.New("network: no physical interface in the host's net/dev")
	}
	return nil
}

func isVirtual(name string) bool {
	for _, prefix := range virtualInterfaces {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// plausibleCelsius drops sensors that report nothing real (unwired inputs).
func plausibleCelsius(celsius float64) bool {
	return celsius > 0 && celsius < 150
}

func readTemperatures(sys string) []Sensor {
	chips, _ := filepath.Glob(filepath.Join(sys, "class", "hwmon", "hwmon*"))
	sort.Strings(chips)
	var sensors []Sensor
	for _, chip := range chips {
		chipName := strings.TrimSpace(readSmall(filepath.Join(chip, "name")))
		inputs, _ := filepath.Glob(filepath.Join(chip, "temp*_input"))
		sort.Strings(inputs)
		for _, input := range inputs {
			milli, err := strconv.ParseFloat(strings.TrimSpace(readSmall(input)), 64)
			if err != nil || !plausibleCelsius(milli/1000) {
				continue
			}
			label := strings.TrimSpace(readSmall(strings.TrimSuffix(input, "_input") + "_label"))
			if label == "" {
				label = strings.TrimSuffix(filepath.Base(input), "_input")
			}
			sensors = append(sensors, Sensor{Sensor: strings.TrimSpace(chipName + " " + label), Celsius: milli / 1000})
		}
	}
	return sensors
}

func readSmall(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}
