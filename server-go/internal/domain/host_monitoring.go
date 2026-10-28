package domain

import "time"

// What a monitored container is to the platform (EXC-670).
const (
	ContainerPlatform   = "platform"
	ContainerDatabase   = "database"
	ContainerDocumentDB = "documentdb"
	ContainerWatcher    = "watcher"
	ContainerApp        = "app"
	ContainerOther      = "other"
)

// ContainerSample is one container's minute on a single host. A nil figure
// was not measured; a container that is not running has none.
type ContainerSample struct {
	Name                 string    `json:"name"`
	ProjectID            string    `json:"projectId"`
	Component            string    `json:"component"`
	State                string    `json:"state"`
	Health               string    `json:"health"`
	Timestamp            time.Time `json:"timestamp"`
	CPUCores             *float64  `json:"cpuCores"`
	CPUPercent           *float64  `json:"cpuPercent"`
	MemoryBytes          *int64    `json:"memoryBytes"`
	MemoryLimitBytes     *int64    `json:"memoryLimitBytes"`
	NetworkRxBytesPerSec *float64  `json:"networkRxBytesPerSec"`
	NetworkTxBytesPerSec *float64  `json:"networkTxBytesPerSec"`
}

// MonitoringAlertSettings are the host's alert rules; a nil threshold is off.
type MonitoringAlertSettings struct {
	Enabled            bool     `json:"enabled"`
	CPUPercent         *float64 `json:"cpuPercent"`
	MemoryPercent      *float64 `json:"memoryPercent"`
	DiskPercent        *float64 `json:"diskPercent"`
	TemperatureCelsius *float64 `json:"temperatureCelsius"`
	Load5              *float64 `json:"load5"`
	NetworkMbps        *float64 `json:"networkMbps"`
	WindowMinutes      int      `json:"windowMinutes"`
	DownMinutes        int      `json:"downMinutes"`
	Emails             []string `json:"emails"`
	// WebhookDisplay is the webhook's scheme and host; the URL is in the vault.
	WebhookDisplay string `json:"-"`
}

// DefaultMonitoringAlertSettings apply until an admin saves their own.
func DefaultMonitoringAlertSettings() MonitoringAlertSettings {
	threshold := func(value float64) *float64 { return &value }
	return MonitoringAlertSettings{
		Enabled: true, CPUPercent: threshold(90), MemoryPercent: threshold(90), DiskPercent: threshold(90),
		TemperatureCelsius: threshold(85), WindowMinutes: 5, DownMinutes: 2, Emails: []string{},
	}
}
