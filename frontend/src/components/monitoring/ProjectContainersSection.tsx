import { useState } from 'react';
import { Loader2 } from 'lucide-react';
import type { MonitoringRange, ProjectContainerSeries } from '../../api/monitoring';
import { useProjectContainerMetrics } from '../../hooks/useMonitoring';
import { containerMemoryLabel, networkLabel } from '../../utils/hostMetrics';
import { ContainerCharts } from './ContainerCharts';
import { ContainerStateBadge } from './ContainerStateBadge';
import { CpuCell } from './ContainersTable';
import { RangePicker } from './RangePicker';

function ContainerCard({
  container,
  range,
}: {
  readonly container: ProjectContainerSeries;
  readonly range: MonitoringRange;
}) {
  const { current } = container;
  return (
    <div
      className="border border-border-primary rounded-lg p-4 space-y-3"
      data-testid={`project-container-${container.name}`}
    >
      <div className="flex items-center gap-3 flex-wrap">
        <span className="font-mono text-xs text-text-primary">{container.name}</span>
        <span className="text-xs text-text-secondary">{container.component}</span>
        <ContainerStateBadge container={container} />
      </div>
      {current && (
        <div className="flex gap-8 text-sm flex-wrap">
          <CpuCell sample={current} />
          <span className="text-text-primary">{containerMemoryLabel(current)}</span>
          <span className="text-text-primary">
            {networkLabel(current.networkRxBytesPerSec, current.networkTxBytesPerSec)}
          </span>
        </div>
      )}
      <ContainerCharts points={container.points} range={range} />
    </div>
  );
}

// The project's containers on a single-host install (EXC-670).
export function ProjectContainersSection({ projectId }: { readonly projectId: string }) {
  const [range, setRange] = useState<MonitoringRange>('1h');
  const { data, isLoading, isError } = useProjectContainerMetrics(projectId, range);
  let body: React.ReactNode;
  if (isLoading) {
    body = <Loader2 className="w-5 h-5 animate-spin text-text-tertiary mx-auto" />;
  } else if (isError || !data) {
    body = <p className="text-sm text-text-secondary">Container metrics could not be loaded.</p>;
  } else if (data.containers.length === 0) {
    body = <p className="text-sm text-text-secondary">No containers for this project.</p>;
  } else {
    body = data.containers.map((container) => (
      <ContainerCard key={container.name} container={container} range={range} />
    ));
  }
  return (
    <div
      className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4"
      data-testid="project-containers"
    >
      <div className="flex items-center justify-between gap-4 flex-wrap">
        <h3 className="text-sm font-semibold text-text-primary">Containers</h3>
        <RangePicker value={range} onChange={setRange} />
      </div>
      {body}
    </div>
  );
}
