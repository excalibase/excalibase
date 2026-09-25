import { Container, Loader2 } from 'lucide-react';
import { useAppHostingEnabled } from '../../hooks/useDeploymentMode';
import { cn } from '../../utils/cn';
import type { Tone } from './appCopy';

const TONE_STYLE: Record<Tone, string> = {
  neutral: 'bg-bg-tertiary text-text-secondary border-border-secondary',
  progress: 'bg-blue-500/10 text-blue-400 border-blue-500/30',
  success: 'bg-green-500/10 text-green-400 border-green-500/30',
  error: 'bg-red-500/10 text-red-400 border-red-500/30',
  warning: 'bg-amber-500/10 text-amber-400 border-amber-500/30',
};

interface ToneBadgeProps {
  readonly label: string;
  readonly tone: Tone;
  readonly testId?: string;
}

export function ToneBadge({ label, tone, testId }: ToneBadgeProps) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium rounded-full border',
        TONE_STYLE[tone],
      )}
      data-testid={testId}
    >
      {tone === 'progress' && <Loader2 className="w-3 h-3 animate-spin" />}
      {label}
    </span>
  );
}

export function Spinner() {
  return (
    <div className="flex justify-center py-12">
      <Loader2 className="w-6 h-6 animate-spin text-purple-400" />
    </div>
  );
}

interface PageHeaderProps {
  readonly title: string;
  readonly subtitle: string;
  readonly actions?: React.ReactNode;
}

export function ContainersHeader({ title, subtitle, actions }: PageHeaderProps) {
  return (
    <div className="flex items-center justify-between gap-4 mb-6">
      <div className="flex items-center gap-3 min-w-0">
        <div className="w-10 h-10 rounded-lg bg-purple-500/10 border border-purple-500/30 flex items-center justify-center flex-shrink-0">
          <Container className="w-5 h-5 text-purple-400" />
        </div>
        <div className="min-w-0">
          <h3 className="text-lg font-semibold text-text-primary truncate">{title}</h3>
          <p className="text-sm text-text-secondary">{subtitle}</p>
        </div>
      </div>
      {actions && <div className="flex items-center gap-2 flex-shrink-0">{actions}</div>}
    </div>
  );
}

// Every Containers page renders through this, so a server with hosting off
// shows an explanation instead of a page whose calls would all 404.
export function HostingGate({ children }: { readonly children: React.ReactNode }) {
  const { enabled, isLoading } = useAppHostingEnabled();
  if (isLoading) return <Spinner />;
  if (!enabled) {
    return (
      <div className="text-center py-12" data-testid="containers-unavailable">
        <p className="text-sm text-text-primary">
          Containers are not available on this installation.
        </p>
        <p className="text-xs text-text-tertiary mt-1">An administrator can turn app hosting on.</p>
      </div>
    );
  }
  return <>{children}</>;
}

export const primaryButton =
  'inline-flex items-center gap-2 px-3 py-2 text-sm rounded-lg bg-purple-500 hover:bg-purple-600 text-white disabled:opacity-50 transition-colors';
export const secondaryButton =
  'inline-flex items-center gap-2 px-3 py-2 text-sm rounded-lg border border-border-primary text-text-secondary hover:bg-surface-hover disabled:opacity-50 transition-colors';
