import { cn } from '../../utils/cn';
import type { ContainerSample } from '../../api/monitoring';
import { containerStateLabel, isContainerHealthy } from '../../utils/hostMetrics';

export function ContainerStateBadge({
  container,
}: {
  readonly container: Pick<ContainerSample, 'state' | 'health'>;
}) {
  const healthy = isContainerHealthy(container);
  return (
    <span
      data-tone={healthy ? 'ok' : 'bad'}
      className={cn(
        'text-xs font-medium px-2 py-0.5 rounded-full whitespace-nowrap',
        healthy ? 'bg-green-500/15 text-green-400' : 'bg-red-500/15 text-red-400',
      )}
    >
      {containerStateLabel(container)}
    </span>
  );
}
