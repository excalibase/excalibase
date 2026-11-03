import { api } from './client';

// Host and container monitoring of a single-host Docker/Podman install (EXC-670).
// Every figure may be null: the sampler could not measure it.
export type MonitoringRange = '1h' | '6h' | '24h' | '7d' | '30d';

export const MONITORING_RANGES: readonly MonitoringRange[] = ['1h', '6h', '24h', '7d', '30d'];

export interface SensorReading {
  sensor: string;
  celsius: number;
}

export interface HostSample {
  timestamp: string;
  cpuPercent: number | null;
  cpuCount: number | null;
  load1: number | null;
  load5: number | null;
  load15: number | null;
  memoryTotalBytes: number | null;
  memoryUsedBytes: number | null;
  swapTotalBytes: number | null;
  swapUsedBytes: number | null;
  diskTotalBytes: number | null;
  diskUsedBytes: number | null;
  diskReadBytesPerSec: number | null;
  diskWriteBytesPerSec: number | null;
  networkRxBytesPerSec: number | null;
  networkTxBytesPerSec: number | null;
  networkSource: 'host' | 'containers' | null;
  temperatureMaxCelsius: number | null;
  temperatures: SensorReading[];
}

export interface HostMonitoring {
  range: MonitoringRange;
  bucketSeconds: number;
  current: HostSample | null;
  // Oldest first.
  points: HostSample[];
}

export type ContainerComponent =
  | 'platform'
  | 'database'
  | 'documentdb'
  | 'watcher'
  | 'app'
  | 'other';
export type ContainerHealth = 'healthy' | 'unhealthy' | 'starting' | '';

export interface ContainerSample {
  name: string;
  projectId: string;
  component: ContainerComponent;
  state: string;
  health: ContainerHealth;
  timestamp: string;
  cpuCores: number | null;
  cpuPercent: number | null;
  memoryBytes: number | null;
  memoryLimitBytes: number | null;
  networkRxBytesPerSec: number | null;
  networkTxBytesPerSec: number | null;
}

export interface ContainersSnapshot {
  timestamp: string | null;
  containers: ContainerSample[];
}

export interface ContainerHistory {
  name: string;
  range: MonitoringRange;
  bucketSeconds: number;
  points: ContainerSample[];
}

export interface AlertThresholds {
  cpuPercent: number | null;
  memoryPercent: number | null;
  diskPercent: number | null;
  temperatureCelsius: number | null;
  load5: number | null;
  networkMbps: number | null;
}

export interface AlertSettings extends AlertThresholds {
  enabled: boolean;
  windowMinutes: number;
  downMinutes: number;
  emails: string[];
  // A masked display value; the URL itself is never sent back.
  webhookUrl: string | null;
  webhookSet: boolean;
}

// webhookUrl: a URL replaces it, "" clears it, leaving it out keeps it.
export interface AlertSettingsUpdate extends AlertThresholds {
  enabled: boolean;
  windowMinutes: number;
  downMinutes: number;
  emails: string[];
  webhookUrl?: string;
}

export interface AlertTestResult {
  channel: 'email' | 'webhook';
  ok: boolean;
  error?: string;
}

export interface AlertTestResponse {
  results: AlertTestResult[];
}

export interface ProjectContainerSeries {
  name: string;
  component: ContainerComponent;
  state: string;
  health: ContainerHealth;
  current: ContainerSample | null;
  points: ContainerSample[];
}

export interface ProjectContainerMetrics {
  range: MonitoringRange;
  bucketSeconds: number;
  containers: ProjectContainerSeries[];
}

const BASE = '/admin/monitoring';

export const getHostMonitoring = async (range: MonitoringRange): Promise<HostMonitoring> =>
  (await api.get<HostMonitoring>(`${BASE}/host?range=${range}`)).data;

export const getMonitoredContainers = async (): Promise<ContainersSnapshot> =>
  (await api.get<ContainersSnapshot>(`${BASE}/containers`)).data;

export const getContainerHistory = async (
  name: string,
  range: MonitoringRange,
): Promise<ContainerHistory> =>
  (await api.get<ContainerHistory>(`${BASE}/containers/${encodeURIComponent(name)}?range=${range}`))
    .data;

export const getAlertSettings = async (): Promise<AlertSettings> =>
  (await api.get<AlertSettings>(`${BASE}/alert-settings`)).data;

export const saveAlertSettings = async (body: AlertSettingsUpdate): Promise<AlertSettings> =>
  (await api.put<AlertSettings>(`${BASE}/alert-settings`, body)).data;

export const sendTestAlert = async (): Promise<AlertTestResponse> =>
  (await api.post<AlertTestResponse>(`${BASE}/alert-settings/test`)).data;

export const getProjectContainerMetrics = async (
  projectId: string,
  range: MonitoringRange,
): Promise<ProjectContainerMetrics> =>
  (
    await api.get<ProjectContainerMetrics>(
      `/projects/${encodeURIComponent(projectId)}/containers/metrics?range=${range}`,
    )
  ).data;
