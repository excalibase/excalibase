import type { HostSample, MonitoringRange } from '../../api/monitoring';
import {
  formatByteRate,
  formatLoad,
  formatWholeCelsius,
  formatWholePercent,
  hostSeries,
} from '../../utils/hostMetrics';
import { MonitoringChart } from './MonitoringChart';

export function HostCharts({
  points,
  range,
}: {
  readonly points: readonly HostSample[];
  readonly range: MonitoringRange;
}) {
  const data = hostSeries(points);
  return (
    <div className="grid grid-cols-1 xl:grid-cols-2 gap-4" data-testid="host-charts">
      <MonitoringChart
        title="CPU"
        data={data}
        range={range}
        format={formatWholePercent}
        lines={[{ key: 'cpu', name: 'CPU %', color: '#3b82f6' }]}
      />
      <MonitoringChart
        title="Memory"
        data={data}
        range={range}
        format={formatWholePercent}
        lines={[{ key: 'memory', name: 'Memory %', color: '#a855f7' }]}
      />
      <MonitoringChart
        title="Disk I/O"
        data={data}
        range={range}
        format={formatByteRate}
        lines={[
          { key: 'diskRead', name: 'Read', color: '#f59e0b' },
          { key: 'diskWrite', name: 'Write', color: '#ef4444' },
        ]}
      />
      <MonitoringChart
        title="Network"
        data={data}
        range={range}
        format={formatByteRate}
        lines={[
          { key: 'rx', name: 'Received', color: '#10b981' },
          { key: 'tx', name: 'Sent', color: '#6366f1' },
        ]}
      />
      <MonitoringChart
        title="Load"
        data={data}
        range={range}
        format={formatLoad}
        lines={[
          { key: 'load1', name: 'Load 1', color: '#0ea5e9' },
          { key: 'load5', name: 'Load 5', color: '#3b82f6' },
          { key: 'load15', name: 'Load 15', color: '#8b5cf6' },
        ]}
      />
      <MonitoringChart
        title="Temperature"
        data={data}
        range={range}
        format={formatWholeCelsius}
        lines={[{ key: 'temperature', name: 'Hottest sensor', color: '#ef4444' }]}
      />
    </div>
  );
}
