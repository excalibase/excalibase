import { Fragment, useState } from 'react';
import { Link } from 'react-router-dom';
import { ChevronDown, ChevronRight, Loader2 } from 'lucide-react';
import type { ContainerSample, MonitoringRange } from '../../api/monitoring';
import { useContainerHistory } from '../../hooks/useMonitoring';
import { containerMemoryLabel, formatFixed, networkLabel } from '../../utils/hostMetrics';
import { ContainerCharts } from './ContainerCharts';
import { ContainerStateBadge } from './ContainerStateBadge';

const COLUMNS = 8;

function ContainerHistoryRow({
  name,
  range,
}: {
  readonly name: string;
  readonly range: MonitoringRange;
}) {
  const { data, isLoading, isError } = useContainerHistory(name, range);
  let body: React.ReactNode;
  if (isLoading) {
    body = <Loader2 className="w-5 h-5 animate-spin text-text-tertiary mx-auto" />;
  } else if (isError || !data) {
    body = <p className="text-sm text-text-secondary">The history could not be loaded.</p>;
  } else {
    body = <ContainerCharts points={data.points} range={range} />;
  }
  return (
    <tr className="border-b border-border-primary bg-bg-secondary" data-testid={`history-${name}`}>
      <td colSpan={COLUMNS} className="px-6 py-4">
        {body}
      </td>
    </tr>
  );
}

export function CpuCell({
  sample,
}: {
  readonly sample: Pick<ContainerSample, 'cpuCores' | 'cpuPercent'>;
}) {
  return (
    <div className="flex flex-col">
      <span className="text-text-primary">{formatFixed(sample.cpuCores, 2, ' cores')}</span>
      <span className="text-xs text-text-tertiary">
        {formatFixed(sample.cpuPercent, 1, '% of host')}
      </span>
    </div>
  );
}

export function ContainersTable({
  containers,
  range,
}: {
  readonly containers: readonly ContainerSample[];
  readonly range: MonitoringRange;
}) {
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const toggle = (name: string) =>
    setOpen((current) => {
      const next = new Set(current);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });

  if (containers.length === 0) {
    return (
      <p className="text-center py-10 text-sm text-text-secondary">No containers sampled yet.</p>
    );
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm" data-testid="containers-table">
        <thead>
          <tr className="text-text-tertiary border-b border-border-primary text-left">
            <th className="px-3 py-3 font-medium w-8">
              <span className="sr-only">History</span>
            </th>
            <th className="px-3 py-3 font-medium">Name</th>
            <th className="px-3 py-3 font-medium">Component</th>
            <th className="px-3 py-3 font-medium">Project</th>
            <th className="px-3 py-3 font-medium">State</th>
            <th className="px-3 py-3 font-medium">CPU</th>
            <th className="px-3 py-3 font-medium">Memory</th>
            <th className="px-3 py-3 font-medium">Network</th>
          </tr>
        </thead>
        <tbody>
          {containers.map((container) => {
            const expanded = open.has(container.name);
            return (
              <Fragment key={container.name}>
                <tr
                  className="border-b border-border-primary hover:bg-surface-hover"
                  data-testid={`container-${container.name}`}
                >
                  <td className="px-3 py-3">
                    <button
                      type="button"
                      aria-expanded={expanded}
                      aria-label={`History of ${container.name}`}
                      onClick={() => toggle(container.name)}
                      className="p-1 rounded text-text-secondary hover:text-text-primary"
                    >
                      {expanded ? (
                        <ChevronDown className="w-4 h-4" />
                      ) : (
                        <ChevronRight className="w-4 h-4" />
                      )}
                    </button>
                  </td>
                  <td className="px-3 py-3 font-mono text-xs text-text-primary">
                    {container.name}
                  </td>
                  <td className="px-3 py-3 text-text-secondary">{container.component}</td>
                  <td className="px-3 py-3">
                    {container.projectId ? (
                      <Link
                        to={`/project/${container.projectId}`}
                        className="text-purple-400 hover:underline"
                      >
                        {container.projectId}
                      </Link>
                    ) : (
                      <span className="text-text-tertiary">—</span>
                    )}
                  </td>
                  <td className="px-3 py-3">
                    <ContainerStateBadge container={container} />
                  </td>
                  <td className="px-3 py-3">
                    <CpuCell sample={container} />
                  </td>
                  <td className="px-3 py-3 text-text-primary">{containerMemoryLabel(container)}</td>
                  <td className="px-3 py-3 text-text-primary whitespace-nowrap">
                    {networkLabel(container.networkRxBytesPerSec, container.networkTxBytesPerSec)}
                  </td>
                </tr>
                {expanded && <ContainerHistoryRow name={container.name} range={range} />}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
