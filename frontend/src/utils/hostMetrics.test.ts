import { describe, test, expect } from 'vitest';
import {
  containerSeries,
  formatLoad,
  formatWholeCelsius,
  formatWholePercent,
  timeTick,
  tooltipValue,
  formatByteRate,
  formatPercent,
  formatSize,
  hostSeries,
  isContainerHealthy,
  memoryPercent,
  timeLabel,
} from './hostMetrics';
import { containerSample, hostSample } from '../test/monitoringFixtures';

describe('host metric formatting', () => {
  test('sizes read in binary units and unmeasured ones as a dash', () => {
    expect(formatSize(null)).toBe('—');
    expect(formatSize(undefined)).toBe('—');
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(512)).toBe('512 B');
    expect(formatSize(1536)).toBe('1.5 KB');
    expect(formatSize(3 * 1024 ** 2)).toBe('3.0 MB');
    expect(formatSize(16 * 1024 ** 3)).toBe('16.0 GB');
    expect(formatSize(2.5 * 1024 ** 4)).toBe('2.5 TB');
  });

  test('rates are sizes per second', () => {
    expect(formatByteRate(2048)).toBe('2.0 KB/s');
    expect(formatByteRate(null)).toBe('—');
  });

  test('percentages keep one decimal', () => {
    expect(formatPercent(42.54)).toBe('42.5%');
    expect(formatPercent(null)).toBe('—');
  });

  test('memory percent needs both figures', () => {
    expect(memoryPercent(hostSample())).toBe(25);
    expect(memoryPercent(hostSample({ memoryTotalBytes: null }))).toBeNull();
    expect(memoryPercent(hostSample({ memoryTotalBytes: 0 }))).toBeNull();
  });

  test('a container is healthy only when running and not unhealthy', () => {
    expect(isContainerHealthy(containerSample())).toBe(true);
    expect(isContainerHealthy(containerSample({ health: '' }))).toBe(true);
    expect(isContainerHealthy(containerSample({ health: 'unhealthy' }))).toBe(false);
    expect(isContainerHealthy(containerSample({ state: 'exited', health: '' }))).toBe(false);
  });

  test('short ranges label the time, long ones the day too', () => {
    const at = '2026-10-29T10:05:00Z';
    expect(timeLabel(at, '1h')).not.toMatch(/Oct|10\/29|29/);
    expect(timeLabel(at, '7d')).toMatch(/29/);
  });
});

describe('chart series', () => {
  test('unmeasured host points stay null so the chart shows a gap', () => {
    const series = hostSeries([
      hostSample(),
      hostSample({
        timestamp: '2026-10-29T10:01:00Z',
        cpuPercent: null,
        memoryUsedBytes: null,
        load5: null,
        temperatureMaxCelsius: null,
      }),
    ]);
    expect(series[0]).toMatchObject({ cpu: 42.5, memory: 25, load5: 0.4, temperature: 61.5 });
    expect(series[1]).toMatchObject({ cpu: null, memory: null, load5: null, temperature: null });
    expect(series[1].cpu).not.toBe(0);
  });

  test('container points keep nulls and memory in bytes', () => {
    const series = containerSeries([
      containerSample(),
      containerSample({
        cpuPercent: null,
        memoryBytes: null,
        memoryLimitBytes: null,
        networkRxBytesPerSec: null,
      }),
    ]);
    expect(series[0]).toMatchObject({ cpu: 6.25, memory: 256 * 1024 ** 2, rx: 2048, tx: 1024 });
    expect(series[1]).toMatchObject({ cpu: null, memory: null, rx: null });
  });
});

describe('chart formatters', () => {
  test('axis values read in their unit', () => {
    expect(formatWholePercent(42.6)).toBe('43%');
    expect(formatLoad(0.4)).toBe('0.40');
    expect(formatWholeCelsius(61.5)).toBe('62 °C');
  });

  test('a tooltip on a gap reads as a dash', () => {
    const format = tooltipValue(formatWholePercent);
    expect(format(12)).toBe('12%');
    expect(format(null)).toBe('—');
  });

  test('ticks label the time for the range', () => {
    expect(timeTick('1h')('2026-10-29T10:05:00Z')).toBe(timeLabel('2026-10-29T10:05:00Z', '1h'));
  });
});
