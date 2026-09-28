import { HardDrive, RefreshCw } from 'lucide-react';
import {
  apiErrorMessage,
  useAppDisk,
  type App,
  type AppDisk,
  type AppDiskStatus,
} from '../../api/apps';
import { formatDiskBytes, sizeToBytes } from '../../utils/diskSize';
import { secondaryButton } from './ContainerBits';
import { AppDiskResize } from './AppDiskResize';
import { cn } from '../../utils/cn';

interface AppDiskCardProps {
  readonly app: App & { disk: AppDisk };
}

const meterColor = (percent: number): string => {
  if (percent >= 90) return 'bg-red-500';
  if (percent >= 75) return 'bg-amber-500';
  return 'bg-purple-500';
};

function measureError(err: unknown): string {
  const status = (err as { response?: { status?: number } } | null)?.response?.status;
  if (status === 409) {
    return 'The disk is busy with another operation on this container; retry in a moment.';
  }
  return apiErrorMessage(err, 'The disk could not be measured');
}

function UsageMeter({ used, size }: { readonly used: number; readonly size: number }) {
  const percent = size > 0 ? Math.min(100, Math.round((used / size) * 100)) : 0;
  return (
    <div
      role="meter"
      aria-label="Disk used"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
      className="h-2 bg-bg-secondary rounded-full overflow-hidden"
    >
      <div className={cn('h-full', meterColor(percent))} style={{ width: `${percent}%` }} />
    </div>
  );
}

function DiskUsage({ status }: { readonly status: AppDiskStatus }) {
  const size = formatDiskBytes(status.sizeBytes);
  return (
    <div className="space-y-2">
      <p className="text-sm text-text-secondary" data-testid="disk-usage">
        {status.usedBytes === undefined
          ? `${size}, not created yet: the next deploy creates it.`
          : `${formatDiskBytes(status.usedBytes)} used of ${size}`}
      </p>
      {status.usedBytes !== undefined && (
        <UsageMeter used={status.usedBytes} size={status.sizeBytes} />
      )}
      <p className="text-xs text-text-tertiary" data-testid="disk-plan-cap">
        {status.planMaxBytes > 0
          ? `The plan allows a disk of up to ${formatDiskBytes(status.planMaxBytes)}.`
          : 'The plan allows no app disks.'}
      </p>
      {status.overPlan && (
        <p
          className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-400"
          data-testid="disk-over-plan"
        >
          This disk is larger than the plan allows ({formatDiskBytes(status.planMaxBytes)}). The
          next deploy or resume makes it that size if its data fits; if not, the container is
          stopped until space is freed on the disk or the organization moves to a larger plan.
        </p>
      )}
    </div>
  );
}

function DiskMeasurement({ disk }: { readonly disk: ReturnType<typeof useAppDisk> }) {
  if (disk.error) {
    return (
      <p className="text-sm text-red-400" data-testid="disk-usage-error">
        {measureError(disk.error)}
      </p>
    );
  }
  if (!disk.data) return <p className="text-sm text-text-tertiary">Measuring the disk…</p>;
  return <DiskUsage status={disk.data} />;
}

// The container's disk: what it holds against its size and the plan cap, and resizing it.
export function AppDiskCard({ app }: AppDiskCardProps) {
  const disk = useAppDisk(app.projectId, app.id);
  const size = sizeToBytes(app.disk.size);
  return (
    <section
      className="bg-surface-card border border-border-primary rounded-lg p-4 space-y-3"
      data-testid="app-disk"
    >
      <div className="flex items-center justify-between gap-2">
        <h4 className="flex items-center gap-2 text-sm font-semibold text-text-primary">
          <HardDrive className="w-4 h-4" /> Disk
        </h4>
        <button
          type="button"
          onClick={() => disk.refetch()}
          disabled={disk.isFetching}
          className={secondaryButton}
          data-testid="disk-refresh"
        >
          <RefreshCw className={cn('w-4 h-4', disk.isFetching && 'animate-spin')} /> Refresh
        </button>
      </div>
      <p className="text-sm text-text-secondary">
        <span className="font-mono">{app.disk.mountPath}</span> ·{' '}
        {size === null ? app.disk.size : formatDiskBytes(size)}
      </p>
      <DiskMeasurement disk={disk} />
      <p className="text-xs text-text-tertiary">
        Kept across restarts, redeploys and pauses. The container runs one copy, so each deploy is
        briefly unavailable while the old copy stops before the new one starts. Deleting the
        container deletes the disk.
      </p>
      <AppDiskResize app={app} usedBytes={disk.data?.usedBytes} />
    </section>
  );
}
