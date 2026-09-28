import { useState } from 'react';
import { HardDrive } from 'lucide-react';
import { apiErrorMessage, useGrowAppDisk, type App, type AppDisk } from '../../api/apps';
import { inputClass } from './EnvVarEditor';
import { secondaryButton } from './ContainerBits';
import { cn } from '../../utils/cn';

const gibibytes = (disk: AppDisk) => Number(disk.size.replace(/Gi$/, ''));

interface AppDiskCardProps {
  readonly app: App & { disk: AppDisk };
}

// The container's disk, and growing it; the server holds it to the plan's cap.
export function AppDiskCard({ app }: AppDiskCardProps) {
  const grow = useGrowAppDisk(app.projectId, app.id);
  const current = gibibytes(app.disk);
  const [size, setSize] = useState(String(current + 1));
  const valid = /^\d+$/.test(size) && Number(size) > current;

  return (
    <section
      className="bg-surface-card border border-border-primary rounded-lg p-4 space-y-3"
      data-testid="app-disk"
    >
      <h4 className="flex items-center gap-2 text-sm font-semibold text-text-primary">
        <HardDrive className="w-4 h-4" /> Disk
      </h4>
      <p className="text-sm text-text-secondary">
        <span className="font-mono">{app.disk.mountPath}</span> · {current} GiB
      </p>
      <p className="text-xs text-text-tertiary">
        Kept across restarts, redeploys and pauses. The container runs one copy, so each deploy is
        briefly unavailable while the old copy stops before the new one starts. Deleting the
        container deletes the disk.
      </p>
      <div className="flex items-center gap-2">
        <input
          type="number"
          min={current + 1}
          value={size}
          onChange={(e) => setSize(e.target.value)}
          className={cn(inputClass, 'w-28')}
          aria-label="New size in GiB"
          data-testid="disk-grow-size"
        />
        <span className="text-sm text-text-secondary">GiB</span>
        <button
          type="button"
          disabled={!valid || grow.isPending}
          onClick={() => grow.mutate(`${Number(size)}Gi`)}
          className={secondaryButton}
          data-testid="disk-grow"
        >
          Grow disk
        </button>
      </div>
      {grow.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(grow.error, 'The disk could not be grown')}
        </p>
      )}
    </section>
  );
}
