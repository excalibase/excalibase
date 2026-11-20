import { Link, type To } from 'react-router-dom';
import { Box, Database, FolderOpen, Globe, Loader2, Zap, type LucideIcon } from 'lucide-react';
import { useDeploys, type App } from '../../../api/apps';
import { useSiteDeploys, type Site } from '../../../api/sites';
import { useTiers } from '../../../api/tiers';
import type { Bucket } from '../../../hooks/useStorage';
import type { DatabaseInstance } from '../../../types';
import { cn } from '../../../utils/cn';
import { ToneBadge } from '../../containers/ContainerBits';
import { databaseServiceStatus } from '../serviceStatus';
import { appCardDetail, appCardStatus, bucketCardDetail, databaseCardDetail, siteCardDetail, siteCardStatus, type CardStatus } from './cardStatus';
import { serviceKey, type ServiceRef } from './panelState';
import type { ProjectServices } from './useProjectServices';

const POLL_MS = 5000;
// Enough history to tell whether an earlier deploy is still the one serving.
const DEPLOYS_READ = 5;

interface CardProps {
  readonly icon: LucideIcon;
  readonly name: string;
  readonly detail: string;
  readonly status?: CardStatus;
  readonly service: ServiceRef;
  readonly selected: boolean;
  readonly to: (service: ServiceRef) => To;
}

function ServiceCard({ icon: Icon, name, detail, status, service, selected, to }: CardProps) {
  return (
    <li>
      <Link
        to={to(service)}
        aria-current={selected ? 'true' : undefined}
        className={cn(
          'flex items-start gap-3 rounded-xl border bg-surface-card p-4 transition-colors hover:bg-surface-hover',
          selected ? 'border-purple-500 ring-1 ring-purple-500/50' : 'border-border-primary',
        )}
      >
        <span className="rounded-lg bg-purple-500/10 p-2 text-purple-400">
          <Icon className="h-5 w-5" aria-hidden="true" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-semibold text-text-primary">{name}</span>
          <span className="block truncate text-xs text-text-tertiary">{detail}</span>
          {status && (
            <span className="mt-2 block">
              <ToneBadge {...status} />
            </span>
          )}
        </span>
      </Link>
    </li>
  );
}

function DatabaseCard({ project, ...rest }: { readonly project: DatabaseInstance } & Pick<CardProps, 'selected' | 'to'>) {
  const { data: tiers } = useTiers();
  const cpu = tiers?.find((tier) => tier.tier === project.tier)?.cpu;
  return (
    <ServiceCard
      icon={Database}
      name="Database"
      detail={databaseCardDetail(project, cpu)}
      status={databaseServiceStatus(project)}
      service={{ kind: 'database' }}
      {...rest}
    />
  );
}

function AppCard({ app, projectId, ...rest }: { readonly app: App; readonly projectId: string } & Pick<CardProps, 'selected' | 'to'>) {
  const { data: deploys } = useDeploys(projectId, app.id, POLL_MS, DEPLOYS_READ);
  return <ServiceCard icon={Box} name={app.name} detail={appCardDetail(app)} status={appCardStatus(app, deploys)} service={{ kind: 'app', id: app.id }} {...rest} />;
}

function SiteCard({ site, projectId, ...rest }: { readonly site: Site; readonly projectId: string } & Pick<CardProps, 'selected' | 'to'>) {
  const { data: deploys } = useSiteDeploys(projectId, site.id);
  return <ServiceCard icon={Globe} name={site.name} detail={siteCardDetail(site)} status={siteCardStatus(site, deploys)} service={{ kind: 'site', id: site.id }} {...rest} />;
}

interface ServiceListProps {
  readonly projectId: string;
  readonly services: ProjectServices;
  readonly selected: ServiceRef | null;
  readonly to: (service: ServiceRef) => To;
}

// One card per service the project holds, each with its own status. The
// selected card is the one whose panel is open.
export function ServiceList({ projectId, services, selected, to }: ServiceListProps) {
  const current = selected ? serviceKey(selected) : '';
  const isSelected = (service: ServiceRef) => serviceKey(service) === current;
  return (
    <ul aria-label="Services" className="space-y-3">
      {services.database && <DatabaseCard project={services.database} selected={isSelected({ kind: 'database' })} to={to} />}
      {services.apps.map((app) => (
        <AppCard key={app.id} app={app} projectId={projectId} selected={isSelected({ kind: 'app', id: app.id })} to={to} />
      ))}
      {services.sites.map((site) => (
        <SiteCard key={site.id} site={site} projectId={projectId} selected={isSelected({ kind: 'site', id: site.id })} to={to} />
      ))}
      {services.functionCount > 0 && (
        <ServiceCard
          icon={Zap}
          name="Functions"
          detail={`${services.functionCount} ${services.functionCount === 1 ? 'function' : 'functions'}`}
          service={{ kind: 'functions' }}
          selected={isSelected({ kind: 'functions' })}
          to={to}
        />
      )}
      {services.buckets.map((bucket: Bucket) => (
        <ServiceCard
          key={bucket.id}
          icon={FolderOpen}
          name={bucket.name}
          detail={bucketCardDetail(bucket)}
          service={{ kind: 'storage', id: bucket.name }}
          selected={isSelected({ kind: 'storage', id: bucket.name })}
          to={to}
        />
      ))}
    </ul>
  );
}

export function ServicesLoading() {
  return (
    <output className="flex justify-center py-10" aria-label="Loading services">
      <Loader2 className="h-6 w-6 animate-spin text-text-tertiary" />
    </output>
  );
}
