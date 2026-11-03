package hostmetrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeHost lays out the files an unprivileged container sees of its host.
func fakeHost(t *testing.T, cpu, diskstats, netdev string) Paths {
	t.Helper()
	root := t.TempDir()
	proc, sys := filepath.Join(root, "proc"), filepath.Join(root, "sys")
	write(t, filepath.Join(proc, "stat"), cpu)
	write(t, filepath.Join(proc, "meminfo"), "MemTotal:       16000000 kB\nMemFree:  1000000 kB\nMemAvailable:    4000000 kB\nSwapTotal:       2000000 kB\nSwapFree:        1500000 kB\n")
	write(t, filepath.Join(proc, "loadavg"), "0.52 1.25 2.00 3/1200 4242\n")
	write(t, filepath.Join(proc, "diskstats"), diskstats)
	write(t, filepath.Join(sys, "block", "nvme0n1", "device", "model"), "disk\n")
	write(t, filepath.Join(sys, "block", "sda", "device", "model"), "disk\n")
	write(t, filepath.Join(sys, "block", "loop0", "size"), "0\n")
	write(t, filepath.Join(sys, "class", "hwmon", "hwmon0", "name"), "coretemp\n")
	write(t, filepath.Join(sys, "class", "hwmon", "hwmon0", "temp1_input"), "51000\n")
	write(t, filepath.Join(sys, "class", "hwmon", "hwmon0", "temp1_label"), "Package id 0\n")
	write(t, filepath.Join(sys, "class", "hwmon", "hwmon0", "temp2_input"), "47500\n")
	write(t, filepath.Join(sys, "class", "hwmon", "hwmon1", "name"), "nvme\n")
	write(t, filepath.Join(sys, "class", "hwmon", "hwmon1", "temp1_input"), "38850\n")
	write(t, filepath.Join(sys, "class", "hwmon", "hwmon2", "name"), "bogus\n")
	write(t, filepath.Join(sys, "class", "hwmon", "hwmon2", "temp1_input"), "-273000\n")
	paths := Paths{Proc: proc, Sys: sys, Disk: root}
	if netdev != "" {
		paths.NetDev = filepath.Join(root, "netdev")
		write(t, paths.NetDev, netdev)
	}
	return paths
}

const netdevHeader = "Inter-|   Receive                                                |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"

func netdev(rx, tx string) string {
	return netdevHeader +
		"    lo: 999999 10 0 0 0 0 0 0 999999 10 0 0 0 0 0 0\n" +
		"  eth0: " + rx + " 10 0 0 0 0 0 0 " + tx + " 10 0 0 0 0 0 0\n" +
		"docker0: 5000 10 0 0 0 0 0 0 5000 10 0 0 0 0 0 0\n" +
		"veth12ab: 7000 10 0 0 0 0 0 0 7000 10 0 0 0 0 0 0\n" +
		"br-1234: 7000 10 0 0 0 0 0 0 7000 10 0 0 0 0 0 0\n"
}

func diskstats(nvmeRead, nvmeWrite string) string {
	return "   7       0 loop0 14 0 100000 1 0 0 100000 0 0 0 1 0 0 0 0 0 0\n" +
		" 259       0 nvme0n1 10 0 " + nvmeRead + " 1 5 0 " + nvmeWrite + " 0 0 0 1 0 0 0 0 0 0\n" +
		" 259       1 nvme0n1p1 10 0 4000 1 5 0 4000 0 0 0 1 0 0 0 0 0 0\n" +
		"   8       0 sda 1 0 1000 1 1 0 1000 0 0 0 1 0 0 0 0 0 0\n"
}

const cpu1 = "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 50 0 50 350 50 0 0 0 0 0\ncpu1 50 0 50 350 50 0 0 0 0 0\nintr 1\n"
const cpu2 = "cpu  250 0 150 1000 100 0 0 0 0 0\ncpu0 125 0 75 500 50 0 0 0 0 0\ncpu1 125 0 75 500 50 0 0 0 0 0\nintr 1\n"

func TestReadTakesTheHostCountersAnUnprivilegedContainerSees(t *testing.T) {
	paths := fakeHost(t, cpu1, diskstats("2000", "6000"), netdev("1000", "3000"))
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	got, problems := Read(paths, now)
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if got.CPUCount != 2 || got.CPUTotal != 1000 || got.CPUIdle != 800 {
		t.Errorf("cpu = %d cores, total %d idle %d", got.CPUCount, got.CPUTotal, got.CPUIdle)
	}
	if got.MemTotal != 16000000*1024 || got.MemAvailable != 4000000*1024 || got.SwapTotal != 2000000*1024 || got.SwapFree != 1500000*1024 {
		t.Errorf("memory = %+v", got)
	}
	if got.Load1 != 0.52 || got.Load5 != 1.25 || got.Load15 != 2.00 {
		t.Errorf("load = %v %v %v", got.Load1, got.Load5, got.Load15)
	}
	// Whole physical disks only: no loop device, no partition counted twice.
	if !got.DiskIOOK || got.DiskReadBytes != (2000+1000)*512 || got.DiskWriteBytes != (6000+1000)*512 {
		t.Errorf("disk io = %v read %d write %d", got.DiskIOOK, got.DiskReadBytes, got.DiskWriteBytes)
	}
	if !got.NetOK || got.NetRx != 1000 || got.NetTx != 3000 {
		t.Errorf("network = %v rx %d tx %d", got.NetOK, got.NetRx, got.NetTx)
	}
	if !got.DiskOK || got.DiskTotal == 0 || got.DiskUsed > got.DiskTotal {
		t.Errorf("disk = %v total %d used %d", got.DiskOK, got.DiskTotal, got.DiskUsed)
	}
	want := []Sensor{{Sensor: "coretemp Package id 0", Celsius: 51}, {Sensor: "coretemp temp2", Celsius: 47.5}, {Sensor: "nvme temp1", Celsius: 38.85}}
	if len(got.Temperatures) != len(want) {
		t.Fatalf("temperatures = %+v", got.Temperatures)
	}
	for i := range want {
		if got.Temperatures[i] != want[i] {
			t.Errorf("temperature %d = %+v, want %+v", i, got.Temperatures[i], want[i])
		}
	}
	if !got.At.Equal(now) {
		t.Errorf("at = %v", got.At)
	}
}

func TestReadWithoutTheHostNetworkFileSaysSo(t *testing.T) {
	paths := fakeHost(t, cpu1, diskstats("0", "0"), "")
	got, problems := Read(paths, time.Now())
	if got.NetOK {
		t.Error("network read without the host's interfaces")
	}
	if len(problems) != 0 {
		t.Errorf("an unset network file is not a problem: %v", problems)
	}
	paths.NetDev = filepath.Join(t.TempDir(), "missing")
	if _, problems := Read(paths, time.Now()); len(problems) != 1 {
		t.Errorf("a configured but unreadable network file is reported: %v", problems)
	}
}

func TestReadReportsWhatItCouldNotRead(t *testing.T) {
	got, problems := Read(Paths{Proc: t.TempDir(), Sys: t.TempDir(), Disk: filepath.Join(t.TempDir(), "none")}, time.Now())
	if len(problems) < 4 {
		t.Errorf("problems = %v, want cpu, memory, load, disk", problems)
	}
	if got.CPUCount != 0 || got.DiskOK || got.DiskIOOK || got.NetOK || len(got.Temperatures) != 0 {
		t.Errorf("nothing was readable, got %+v", got)
	}
	sample := Rates(nil, got)
	if sample.CPUPercent != nil || sample.MemoryTotalBytes != nil || sample.Load1 != nil || sample.DiskTotalBytes != nil || sample.TemperatureMaxCelsius != nil {
		t.Errorf("an unread figure must be nil, got %+v", sample)
	}
}

func TestRatesAreDeltasOverTheIntervalAndTheFirstSampleHasNone(t *testing.T) {
	paths := fakeHost(t, cpu1, diskstats("2000", "6000"), netdev("1000", "3000"))
	start := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	first, _ := Read(paths, start)

	sample := Rates(nil, first)
	if sample.CPUPercent != nil || sample.DiskReadBytesPerSec != nil || sample.NetworkRxBytesPerSec != nil {
		t.Errorf("a first sample has no rates: %+v", sample)
	}
	if sample.MemoryUsedBytes == nil || *sample.MemoryUsedBytes != 12000000*1024 || *sample.SwapUsedBytes != 500000*1024 {
		t.Errorf("memory = %+v", sample)
	}
	if *sample.CPUCount != 2 || *sample.Load5 != 1.25 || *sample.TemperatureMaxCelsius != 51 || sample.NetworkSource == nil || *sample.NetworkSource != NetworkFromHost {
		t.Errorf("sample = %+v", sample)
	}

	write(t, filepath.Join(paths.Proc, "stat"), cpu2)
	write(t, filepath.Join(paths.Proc, "diskstats"), diskstats("2120", "6240"))
	write(t, paths.NetDev, netdev("7000", "9000"))
	second, _ := Read(paths, start.Add(60*time.Second))
	sample = Rates(&first, second)
	// total 1500-1000=500 jiffies, idle 1100-800=300: 40% busy.
	if sample.CPUPercent == nil || *sample.CPUPercent != 40 {
		t.Errorf("cpu = %v, want 40", sample.CPUPercent)
	}
	if *sample.DiskReadBytesPerSec != 120*512/60.0 || *sample.DiskWriteBytesPerSec != 240*512/60.0 {
		t.Errorf("disk io = %v %v", *sample.DiskReadBytesPerSec, *sample.DiskWriteBytesPerSec)
	}
	if *sample.NetworkRxBytesPerSec != 100 || *sample.NetworkTxBytesPerSec != 100 {
		t.Errorf("network = %v %v", *sample.NetworkRxBytesPerSec, *sample.NetworkTxBytesPerSec)
	}
}

func TestACounterThatWentBackwardsGivesNoRate(t *testing.T) {
	start := time.Now()
	prev := Counters{At: start, CPUTotal: 1000, CPUIdle: 500, CPUCount: 2, NetOK: true, NetRx: 9000, NetTx: 9000, DiskIOOK: true, DiskReadBytes: 100, DiskWriteBytes: 100}
	cur := Counters{At: start.Add(time.Minute), CPUTotal: 10, CPUIdle: 5, CPUCount: 2, NetOK: true, NetRx: 10, NetTx: 10, DiskIOOK: true, DiskReadBytes: 1, DiskWriteBytes: 1}
	sample := Rates(&prev, cur)
	if sample.CPUPercent != nil || sample.NetworkRxBytesPerSec != nil || sample.DiskReadBytesPerSec != nil {
		t.Errorf("a reset counter (host reboot) must give no rate: %+v", sample)
	}
	if Rates(&cur, cur).CPUPercent != nil {
		t.Error("no time between samples gives no rate")
	}
}
