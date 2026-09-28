import { useState } from 'react';
import { apiErrorMessage, useResizeAppDisk, type App, type AppDisk } from '../../api/apps';
import {
  MIN_DISK_MI,
  formatDiskBytes,
  sizeToBytes,
  splitDiskSize,
  toDiskSize,
  type DiskUnit,
} from '../../utils/diskSize';
import { isAppRunning } from './appCopy';
import { inputClass } from './EnvVarEditor';
import { secondaryButton } from './ContainerBits';
import { cn } from '../../utils/cn';

interface AppDiskResizeProps {
  readonly app: App & { disk: AppDisk };
  readonly usedBytes?: number;
}

function resizeHint(running: boolean, usedBytes?: number): string {
  const units = `Whole Mi (at least ${MIN_DISK_MI}Mi) or whole Gi, up to the plan's cap.`;
  if (running) return `${units} Stop the container to make its disk smaller.`;
  if (usedBytes === undefined) return `${units} It can be made smaller while stopped.`;
  return `${units} It can be made smaller while stopped, but not below what it holds: ${formatDiskBytes(usedBytes)}.`;
}

// Growing works any time; lowering copies the disk, so the server only does it while stopped.
export function AppDiskResize({ app, usedBytes }: AppDiskResizeProps) {
  const resize = useResizeAppDisk(app.projectId, app.id);
  const initial = splitDiskSize(app.disk.size);
  const [amount, setAmount] = useState(initial.amount);
  const [unit, setUnit] = useState<DiskUnit>(initial.unit);
  const running = isAppRunning(app);
  const current = sizeToBytes(app.disk.size);
  const target = toDiskSize(amount, unit);
  const lowering = target !== null && current !== null && target.bytes < current;
  const valid = target !== null && target.bytes !== current && !(lowering && running);

  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2">
        <input
          type="number"
          min={1}
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
          className={cn(inputClass, 'w-28')}
          aria-label="New disk size"
          data-testid="disk-resize-size"
        />
        <select
          value={unit}
          onChange={(e) => setUnit(e.target.value as DiskUnit)}
          className={cn(inputClass, 'w-20')}
          aria-label="Unit"
          data-testid="disk-resize-unit"
        >
          <option value="Mi">Mi</option>
          <option value="Gi">Gi</option>
        </select>
        <button
          type="button"
          disabled={!valid || resize.isPending}
          onClick={() => target && resize.mutate(target.size)}
          className={secondaryButton}
          data-testid="disk-resize"
        >
          {lowering ? 'Make disk smaller' : 'Resize disk'}
        </button>
      </div>
      <p className="text-xs text-text-tertiary" data-testid="disk-resize-hint">
        {resizeHint(running, usedBytes)}
      </p>
      {resize.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(resize.error, 'The disk could not be resized')}
        </p>
      )}
    </div>
  );
}
