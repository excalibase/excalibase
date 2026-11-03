import { Activity, Cpu, HardDrive, MemoryStick, Network, Thermometer, Gauge } from 'lucide-react';
import { MetricCard } from '../shared/MetricCard';
import type { HostSample } from '../../api/monitoring';
import {
  formatByteRate,
  formatCelsius,
  formatFixed,
  formatPercent,
  formatSize,
  networkLabel,
} from '../../utils/hostMetrics';

const CONTAINERS_NOTE = "host interfaces not readable; showing the containers' sum";

function swapLabel(sample: HostSample): string {
  if (!sample.swapTotalBytes) return 'No swap';
  return `Swap ${formatSize(sample.swapUsedBytes)} / ${formatSize(sample.swapTotalBytes)}`;
}

function diskPercent(sample: HostSample): string | undefined {
  const { diskUsedBytes: used, diskTotalBytes: total } = sample;
  if (used == null || total == null || total <= 0) return undefined;
  return `${formatPercent((used / total) * 100)} used`;
}

export function HostCards({ sample }: { readonly sample: HostSample }) {
  const cards = [
    {
      id: 'cpu',
      icon: <Cpu className="w-4 h-4" />,
      label: 'CPU',
      value: formatPercent(sample.cpuPercent),
      subtitle: sample.cpuCount == null ? undefined : `${sample.cpuCount} cores`,
      color: 'text-blue-400',
    },
    {
      id: 'memory',
      icon: <MemoryStick className="w-4 h-4" />,
      label: 'Memory',
      value: `${formatSize(sample.memoryUsedBytes)} / ${formatSize(sample.memoryTotalBytes)}`,
      subtitle: swapLabel(sample),
      color: 'text-purple-400',
    },
    {
      id: 'disk',
      icon: <HardDrive className="w-4 h-4" />,
      label: 'Disk',
      value: `${formatSize(sample.diskUsedBytes)} / ${formatSize(sample.diskTotalBytes)}`,
      subtitle: diskPercent(sample),
      color: 'text-orange-400',
    },
    {
      id: 'disk-io',
      icon: <Activity className="w-4 h-4" />,
      label: 'Disk I/O',
      value: `R ${formatByteRate(sample.diskReadBytesPerSec)} · W ${formatByteRate(sample.diskWriteBytesPerSec)}`,
      subtitle: undefined,
      color: 'text-amber-400',
    },
    {
      id: 'network',
      icon: <Network className="w-4 h-4" />,
      label: 'Network',
      value: networkLabel(sample.networkRxBytesPerSec, sample.networkTxBytesPerSec),
      subtitle: sample.networkSource === 'containers' ? CONTAINERS_NOTE : undefined,
      color: 'text-emerald-400',
    },
    {
      id: 'load',
      icon: <Gauge className="w-4 h-4" />,
      label: 'Load 1 / 5 / 15',
      value: [sample.load1, sample.load5, sample.load15]
        .map((load) => formatFixed(load, 2))
        .join(' / '),
      subtitle: undefined,
      color: 'text-sky-400',
    },
    {
      id: 'temperature',
      icon: <Thermometer className="w-4 h-4" />,
      label: 'Temperature',
      value: formatCelsius(sample.temperatureMaxCelsius),
      subtitle: sample.temperatureMaxCelsius == null ? undefined : 'hottest sensor',
      color: 'text-red-400',
    },
  ];
  return (
    <div className="space-y-4">
      <div
        className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-4"
        data-testid="host-cards"
      >
        {cards.map(({ id, ...card }) => (
          <div key={id} data-testid={`card-${id}`}>
            <MetricCard {...card} className="h-full" />
          </div>
        ))}
      </div>
      {sample.temperatures.length > 0 && (
        <div
          className="bg-surface-card border border-border-primary rounded-xl p-5"
          data-testid="host-sensors"
        >
          <h3 className="text-sm font-semibold text-text-primary mb-3">Sensors</h3>
          <ul className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2 text-sm">
            {sample.temperatures.map((reading) => (
              <li key={reading.sensor} className="flex justify-between gap-4">
                <span className="font-mono text-xs text-text-secondary">{reading.sensor}</span>
                <span className="text-text-primary">{formatCelsius(reading.celsius)}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
