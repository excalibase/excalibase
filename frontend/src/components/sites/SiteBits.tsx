import { Link } from 'react-router-dom';
import { Globe } from 'lucide-react';
import { useSitesEnabled } from '../../hooks/useDeploymentMode';
import type { SiteDeploy, SiteLimits } from '../../api/sites';
import type { Tone } from '../containers/appCopy';
import { Spinner } from '../containers/ContainerBits';

interface PageHeaderProps {
  readonly title: string;
  readonly subtitle: string;
  readonly actions?: React.ReactNode;
}

export function SitesHeader({ title, subtitle, actions }: PageHeaderProps) {
  return (
    <div className="flex items-center justify-between gap-4 mb-6">
      <div className="flex items-center gap-3 min-w-0">
        <div className="w-10 h-10 rounded-lg bg-purple-500/10 border border-purple-500/30 flex items-center justify-center flex-shrink-0">
          <Globe className="w-5 h-5 text-purple-400" />
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

// Every Sites page renders through this: an install without Sites points the
// frontend at a container instead of calling routes that answer 503.
export function SitesGate({
  projectId,
  children,
}: {
  readonly projectId: string;
  readonly children: React.ReactNode;
}) {
  const { enabled, isLoading } = useSitesEnabled();
  if (isLoading) return <Spinner />;
  if (!enabled) {
    return (
      <div className="text-center py-12" data-testid="sites-unavailable">
        <p className="text-sm text-text-primary">
          Static frontend hosting isn&apos;t available on this install; deploy your frontend as a{' '}
          <Link to={`/project/${projectId}/containers`} className="text-purple-400 hover:underline">
            container
          </Link>
          .
        </p>
      </div>
    );
  }
  return <>{children}</>;
}

export function deployStatus(deploy: SiteDeploy): { label: string; tone: Tone } {
  if (deploy.live) return { label: 'Live', tone: 'success' };
  if (deploy.status === 'PENDING') return { label: 'Publishing', tone: 'progress' };
  if (deploy.status === 'FAILED') return { label: 'Failed', tone: 'error' };
  return { label: 'Succeeded', tone: 'neutral' };
}

// Limits are set in whole mebibytes; one decimal covers an admin's odd value.
const mebibytes = (bytes: number) => `${Number((bytes / 1048576).toFixed(1))} MiB`;

// The plan's Sites limits (EXC-620), so a refused create or deploy is no surprise.
export function SiteLimitsNote({
  limits,
  held,
}: {
  readonly limits: SiteLimits;
  readonly held: number;
}) {
  return (
    <p className="mt-4 text-xs text-text-tertiary" data-testid="sites-limits">
      {held} of {limits.maxSites} sites on your plan. Each deploy up to{' '}
      {mebibytes(limits.maxDeployBytes)}, {limits.maxFiles.toLocaleString('en-US')} files,{' '}
      {mebibytes(limits.maxFileBytes)} per file; {limits.maxDeploysPerHour} deploys an hour per
      project.
    </p>
  );
}
