package hostmetrics

import "time"

// Where a sample's network figures come from.
const (
	NetworkFromHost       = "host"
	NetworkFromContainers = "containers"
)

// Sample is one host row: gauges as read, rates over the interval since the
// previous reading. A nil figure was not measured, which is not a zero.
type Sample struct {
	Timestamp             time.Time `json:"timestamp"`
	CPUPercent            *float64  `json:"cpuPercent"`
	CPUCount              *int      `json:"cpuCount"`
	Load1                 *float64  `json:"load1"`
	Load5                 *float64  `json:"load5"`
	Load15                *float64  `json:"load15"`
	MemoryTotalBytes      *int64    `json:"memoryTotalBytes"`
	MemoryUsedBytes       *int64    `json:"memoryUsedBytes"`
	SwapTotalBytes        *int64    `json:"swapTotalBytes"`
	SwapUsedBytes         *int64    `json:"swapUsedBytes"`
	DiskTotalBytes        *int64    `json:"diskTotalBytes"`
	DiskUsedBytes         *int64    `json:"diskUsedBytes"`
	DiskReadBytesPerSec   *float64  `json:"diskReadBytesPerSec"`
	DiskWriteBytesPerSec  *float64  `json:"diskWriteBytesPerSec"`
	NetworkRxBytesPerSec  *float64  `json:"networkRxBytesPerSec"`
	NetworkTxBytesPerSec  *float64  `json:"networkTxBytesPerSec"`
	NetworkSource         *string   `json:"networkSource"`
	TemperatureMaxCelsius *float64  `json:"temperatureMaxCelsius"`
	Temperatures          []Sensor  `json:"temperatures"`
}

// MemoryPercent is the share of memory in use, when both figures are known.
func (s Sample) MemoryPercent() *float64 {
	return share(s.MemoryUsedBytes, s.MemoryTotalBytes)
}

// DiskPercent is the share of the engine's filesystem in use.
func (s Sample) DiskPercent() *float64 {
	return share(s.DiskUsedBytes, s.DiskTotalBytes)
}

func share(used, total *int64) *float64 {
	if used == nil || total == nil || *total <= 0 {
		return nil
	}
	return ptr(float64(*used) / float64(*total) * 100)
}

// Rates turns a reading into a sample, with rates against prev when there is one.
func Rates(prev *Counters, cur Counters) Sample {
	sample := Sample{Timestamp: cur.At, Temperatures: cur.Temperatures}
	if cur.CPUCount > 0 {
		sample.CPUCount = ptr(cur.CPUCount)
	}
	if cur.LoadOK {
		sample.Load1, sample.Load5, sample.Load15 = ptr(cur.Load1), ptr(cur.Load5), ptr(cur.Load15)
	}
	if cur.MemOK {
		sample.MemoryTotalBytes = bytesPtr(cur.MemTotal)
		sample.MemoryUsedBytes = bytesPtr(cur.MemTotal - min(cur.MemAvailable, cur.MemTotal))
		sample.SwapTotalBytes = bytesPtr(cur.SwapTotal)
		sample.SwapUsedBytes = bytesPtr(cur.SwapTotal - min(cur.SwapFree, cur.SwapTotal))
	}
	if cur.DiskOK {
		sample.DiskTotalBytes, sample.DiskUsedBytes = bytesPtr(cur.DiskTotal), bytesPtr(cur.DiskUsed)
	}
	if cur.NetOK {
		sample.NetworkSource = ptr(NetworkFromHost)
	}
	for _, sensor := range cur.Temperatures {
		if sample.TemperatureMaxCelsius == nil || sensor.Celsius > *sample.TemperatureMaxCelsius {
			sample.TemperatureMaxCelsius = ptr(sensor.Celsius)
		}
	}
	if prev != nil {
		addRates(*prev, cur, &sample)
	}
	return sample
}

func addRates(prev, cur Counters, sample *Sample) {
	seconds := cur.At.Sub(prev.At).Seconds()
	if seconds <= 0 {
		return
	}
	if cur.CPUCount > 0 && prev.CPUCount > 0 && cur.CPUTotal > prev.CPUTotal && cur.CPUIdle >= prev.CPUIdle {
		total := float64(cur.CPUTotal - prev.CPUTotal)
		idle := float64(cur.CPUIdle - prev.CPUIdle)
		sample.CPUPercent = ptr(max(0, (total-idle)/total*100))
	}
	if cur.DiskIOOK && prev.DiskIOOK {
		sample.DiskReadBytesPerSec = rate(prev.DiskReadBytes, cur.DiskReadBytes, seconds)
		sample.DiskWriteBytesPerSec = rate(prev.DiskWriteBytes, cur.DiskWriteBytes, seconds)
	}
	if cur.NetOK && prev.NetOK {
		sample.NetworkRxBytesPerSec = rate(prev.NetRx, cur.NetRx, seconds)
		sample.NetworkTxBytesPerSec = rate(prev.NetTx, cur.NetTx, seconds)
	}
}

// rate is nil for a counter that went backwards: a reboot or a device that left.
func rate(prev, cur uint64, seconds float64) *float64 {
	if cur < prev {
		return nil
	}
	return ptr(float64(cur-prev) / seconds)
}

func bytesPtr(value uint64) *int64 {
	return ptr(int64(min(value, uint64(1<<63-1))))
}

func ptr[T any](value T) *T { return &value }

// Averages are the host's figures averaged over an alert window.
type Averages struct {
	Samples            int
	CPUPercent         *float64
	MemoryPercent      *float64
	DiskPercent        *float64
	TemperatureCelsius *float64
	Load5              *float64
	// NetworkBytesPerSec is received plus sent.
	NetworkBytesPerSec *float64
}
