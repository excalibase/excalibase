import { cn } from '../utils/cn';
import type { RealtimeStatus } from '../realtime/useGraphqlRealtime';

interface RealtimeIndicatorProps {
  readonly status: RealtimeStatus;
  readonly className?: string;
}

interface BadgeStyle {
  readonly label: string;
  readonly dotClass: string;
  readonly textClass: string;
  readonly bgClass: string;
  readonly borderClass: string;
}

function styleFor(status: RealtimeStatus): BadgeStyle {
  switch (status) {
    case 'live':
      return {
        label: 'Live',
        dotClass: 'bg-green-400',
        textClass: 'text-green-400',
        bgClass: 'bg-green-500/10',
        borderClass: 'border-green-500/30',
      };
    case 'connecting':
      return {
        label: 'Connecting...',
        dotClass: 'bg-blue-400 animate-pulse',
        textClass: 'text-blue-400',
        bgClass: 'bg-blue-500/10',
        borderClass: 'border-blue-500/30',
      };
    case 'reconnecting':
      return {
        label: 'Reconnecting...',
        dotClass: 'bg-amber-400 animate-pulse',
        textClass: 'text-amber-400',
        bgClass: 'bg-amber-500/10',
        borderClass: 'border-amber-500/30',
      };
    case 'offline':
    default:
      return {
        label: 'Offline',
        dotClass: 'bg-red-400',
        textClass: 'text-red-400',
        bgClass: 'bg-red-500/10',
        borderClass: 'border-red-500/30',
      };
  }
}

/**
 * Status badge for live data subscriptions. Visual conventions match the
 * existing StatusBadge — small pill with a coloured dot and label.
 */
export function RealtimeIndicator({ status, className }: RealtimeIndicatorProps) {
  const style = styleFor(status);
  return (
    <span
      data-testid="realtime-indicator"
      className={cn(
        'inline-flex items-center gap-1.5 px-2.5 py-1 text-xs font-medium rounded-full border',
        style.bgClass,
        style.textClass,
        style.borderClass,
        className,
      )}
    >
      <span className={cn('w-1.5 h-1.5 rounded-full', style.dotClass)} aria-hidden />
      {style.label}
    </span>
  );
}
