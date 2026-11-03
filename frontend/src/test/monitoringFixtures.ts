import type { AlertSettings, ContainerSample, HostSample } from '../api/monitoring';

const GIB = 1024 ** 3;
const MIB = 1024 ** 2;

export const hostSample = (overrides: Partial<HostSample> = {}): HostSample => ({
  timestamp: '2026-10-29T10:00:00Z',
  cpuPercent: 42.5,
  cpuCount: 4,
  load1: 0.52,
  load5: 0.4,
  load15: 0.33,
  memoryTotalBytes: 16 * GIB,
  memoryUsedBytes: 4 * GIB,
  swapTotalBytes: 2 * GIB,
  swapUsedBytes: 512 * MIB,
  diskTotalBytes: 100 * GIB,
  diskUsedBytes: 25 * GIB,
  diskReadBytesPerSec: 2 * MIB,
  diskWriteBytesPerSec: 512 * 1024,
  networkRxBytesPerSec: 1024,
  networkTxBytesPerSec: 3 * MIB,
  networkSource: 'host',
  temperatureMaxCelsius: 61.5,
  temperatures: [
    { sensor: 'coretemp_package', celsius: 61.5 },
    { sensor: 'nvme_composite', celsius: 44 },
  ],
  ...overrides,
});

export const containerSample = (overrides: Partial<ContainerSample> = {}): ContainerSample => ({
  name: 'excalibase-provisioning',
  projectId: '',
  component: 'platform',
  state: 'running',
  health: 'healthy',
  timestamp: '2026-10-29T10:00:00Z',
  cpuCores: 0.25,
  cpuPercent: 6.25,
  memoryBytes: 256 * MIB,
  memoryLimitBytes: 1 * GIB,
  networkRxBytesPerSec: 2048,
  networkTxBytesPerSec: 1024,
  ...overrides,
});

export const alertSettings = (overrides: Partial<AlertSettings> = {}): AlertSettings => ({
  enabled: true,
  cpuPercent: 90,
  memoryPercent: 85,
  diskPercent: null,
  temperatureCelsius: null,
  load5: null,
  networkMbps: null,
  windowMinutes: 5,
  downMinutes: 3,
  emails: ['ops@example.test'],
  webhookUrl: 'https://hooks.example.test/…abcd',
  webhookSet: true,
  ...overrides,
});
