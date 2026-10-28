import type { ContainerSample, MonitoringRange } from '../../api/monitoring';
import {
  containerSeries,
  formatByteRate,
  formatPercent,
  formatSize,
} from '../../utils/hostMetrics';
import { MonitoringChart } from './MonitoringChart';

// CPU, memory and network of one container over the range.
export function ContainerCharts({
  points,
  range,
}: {
  readonly points: readonly ContainerSample[];
  readonly range: MonitoringRange;
}) {
  if (points.length === 0) {
    return <p className="text-sm text-text-secondary py-4">No samples in this range.</p>;
  }
  const data = containerSeries(points);
  return (
    <div className="grid grid-cols-1 lg:grid-cols-3 gap-3">
      <MonitoringChart
        title="CPU"
        data={data}
        range={range}
        height={160}
        format={formatPercent}
        lines={[{ key: 'cpu', name: '% of host', color: '#3b82f6' }]}
      />
      <MonitoringChart
        title="Memory"
        data={data}
        range={range}
        height={160}
        format={formatSize}
        lines={[{ key: 'memory', name: 'Used', color: '#a855f7' }]}
      />
      <MonitoringChart
        title="Network"
        data={data}
        range={range}
        height={160}
        format={formatByteRate}
        lines={[
          { key: 'rx', name: 'Received', color: '#10b981' },
          { key: 'tx', name: 'Sent', color: '#6366f1' },
        ]}
      />
    </div>
  );
}
