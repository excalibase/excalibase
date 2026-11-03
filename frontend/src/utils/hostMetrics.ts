import type { ContainerSample, HostSample, MonitoringRange } from '../api/monitoring';

const DASH = '—';
const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const;

// Binary units; an unmeasured size reads as a dash, a real zero as "0 B".
export function formatSize(bytes: number | null | undefined): string {
  if (bytes == null) return DASH;
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return unit === 0 ? `${Math.round(value)} B` : `${value.toFixed(1)} ${UNITS[unit]}`;
}

export function formatByteRate(bytesPerSec: number | null | undefined): string {
  return bytesPerSec == null ? DASH : `${formatSize(bytesPerSec)}/s`;
}

export function formatPercent(percent: number | null | undefined): string {
  return percent == null ? DASH : `${percent.toFixed(1)}%`;
}

export function formatFixed(value: number | null | undefined, digits: number, suffix = ''): string {
  return value == null ? DASH : `${value.toFixed(digits)}${suffix}`;
}

export function formatCelsius(celsius: number | null | undefined): string {
  return formatFixed(celsius, 1, ' °C');
}

export function memoryPercent(sample: HostSample): number | null {
  const { memoryUsedBytes: used, memoryTotalBytes: total } = sample;
  if (used == null || total == null || total <= 0) return null;
  return (used / total) * 100;
}

// Not running, or running but failing its health check.
export function isContainerHealthy(container: Pick<ContainerSample, 'state' | 'health'>): boolean {
  return container.state === 'running' && container.health !== 'unhealthy';
}

export function containerStateLabel(container: Pick<ContainerSample, 'state' | 'health'>): string {
  return container.health ? `${container.state} · ${container.health}` : container.state;
}

export function containerMemoryLabel(
  sample: Pick<ContainerSample, 'memoryBytes' | 'memoryLimitBytes'>,
): string {
  const limit = sample.memoryLimitBytes == null ? 'no limit' : formatSize(sample.memoryLimitBytes);
  return `${formatSize(sample.memoryBytes)} / ${limit}`;
}

export function networkLabel(rx: number | null, tx: number | null): string {
  return `↓ ${formatByteRate(rx)} · ↑ ${formatByteRate(tx)}`;
}

const LONG_RANGES = new Set<MonitoringRange>(['7d', '30d']);

export function timeLabel(timestamp: string, range: MonitoringRange): string {
  const at = new Date(timestamp);
  if (LONG_RANGES.has(range)) {
    return at.toLocaleString([], {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
  }
  return at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

export interface HostPoint {
  timestamp: string;
  cpu: number | null;
  memory: number | null;
  diskRead: number | null;
  diskWrite: number | null;
  rx: number | null;
  tx: number | null;
  load1: number | null;
  load5: number | null;
  load15: number | null;
  temperature: number | null;
}

// Nulls are kept: the charts draw them as gaps, never as zero.
export function hostSeries(points: readonly HostSample[]): HostPoint[] {
  return points.map((point) => ({
    timestamp: point.timestamp,
    cpu: point.cpuPercent,
    memory: memoryPercent(point),
    diskRead: point.diskReadBytesPerSec,
    diskWrite: point.diskWriteBytesPerSec,
    rx: point.networkRxBytesPerSec,
    tx: point.networkTxBytesPerSec,
    load1: point.load1,
    load5: point.load5,
    load15: point.load15,
    temperature: point.temperatureMaxCelsius,
  }));
}

export interface ContainerPoint {
  timestamp: string;
  cpu: number | null;
  memory: number | null;
  rx: number | null;
  tx: number | null;
}

export function containerSeries(points: readonly ContainerSample[]): ContainerPoint[] {
  return points.map((point) => ({
    timestamp: point.timestamp,
    cpu: point.cpuPercent,
    memory: point.memoryBytes,
    rx: point.networkRxBytesPerSec,
    tx: point.networkTxBytesPerSec,
  }));
}

// Axis and tooltip formatters for the monitoring charts.
export const formatWholePercent = (value: number): string => `${Math.round(value)}%`;
export const formatLoad = (value: number): string => value.toFixed(2);
export const formatWholeCelsius = (value: number): string => `${Math.round(value)} °C`;

export function timeTick(range: MonitoringRange): (timestamp: unknown) => string {
  return (timestamp) => timeLabel(String(timestamp), range);
}

export function tooltipValue(format: (value: number) => string): (value: unknown) => string {
  return (value) => (typeof value === 'number' ? format(value) : DASH);
}
