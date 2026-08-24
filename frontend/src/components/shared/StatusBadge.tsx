import { CheckCircle, AlertTriangle, XCircle, Loader2, MinusCircle, PauseCircle } from 'lucide-react';
import { cn } from '../../utils/cn';
import type { ProvisioningStage } from '../../types';

interface StatusBadgeProps {
  status?: string;
  stage?: ProvisioningStage;
  className?: string;
}

export function StatusBadge({ status, stage, className }: StatusBadgeProps) {
  const value = stage ?? status ?? '';

  const badge = (style: string, icon: React.ReactNode, label: string) => (
    <span className={cn('inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium rounded-full border', style, className)}>
      {icon} {label}
    </span>
  );

  if (value === 'FAILED')
    return badge('bg-red-500/10 text-red-400 border-red-500/30',   <XCircle className="w-3 h-3" />,       'Failed');
  if (value === 'COMPLETED' || value === 'ACTIVE')
    return badge('bg-green-500/10 text-green-400 border-green-500/30', <CheckCircle className="w-3 h-3" />,  'Active');
  if (value === 'HEALTHY')
    return badge('bg-green-500/10 text-green-400 border-green-500/30', <CheckCircle className="w-3 h-3" />,  'Healthy');
  if (value === 'DEGRADED')
    return badge('bg-amber-500/10 text-amber-400 border-amber-500/30', <AlertTriangle className="w-3 h-3" />, 'Degraded');
  if (value === 'DOWN')
    return badge('bg-red-500/10 text-red-400 border-red-500/30',   <XCircle className="w-3 h-3" />,       'Down');
  if (value === 'DEPROVISIONED')
    return badge('bg-bg-tertiary text-text-tertiary border-border-secondary', <MinusCircle className="w-3 h-3" />, 'Deprovisioned');
  if (value === 'PAUSED')
    return badge('bg-amber-500/10 text-amber-400 border-amber-500/30', <PauseCircle className="w-3 h-3" />, 'Paused');
  if (value === 'PAUSING' || value === 'RESUMING')
    return badge('bg-blue-500/10 text-blue-400 border-blue-500/30', <Loader2 className="w-3 h-3 animate-spin" />, value === 'PAUSING' ? 'Pausing' : 'Resuming');

  // Provisioning / in-progress
  return badge('bg-blue-500/10 text-blue-400 border-blue-500/30', <Loader2 className="w-3 h-3 animate-spin" />, 'Provisioning');
}
