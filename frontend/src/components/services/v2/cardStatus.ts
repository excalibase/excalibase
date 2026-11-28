import type { App, Deploy } from '../../../api/apps';
import type { Site, SiteDeploy } from '../../../api/sites';
import type { DatabaseInstance } from '../../../types';
import type { RuntimeStatus } from '../../../types/edgefn';
import { appDisplayStatus, type Tone } from '../../containers/appCopy';

export interface CardStatus {
  readonly label: string;
  readonly tone: Tone;
}

const FAILED_LIVE_ON_PREVIOUS: CardStatus = { label: 'Last deploy failed · live on previous', tone: 'warning' };

// Deploys come newest first. A failed newest deploy does not take the service
// down while an earlier one still serves, so the card says both.
export function appCardStatus(app: App, deploys: readonly Deploy[] | undefined): CardStatus {
  const [newest, ...earlier] = deploys ?? [];
  const status = appDisplayStatus(app, newest);
  const serving = app.status === 'ACTIVE' && app.replicas !== 0;
  if (serving && newest?.status === 'failed' && earlier.some((d) => d.status === 'succeeded')) return FAILED_LIVE_ON_PREVIOUS;
  return status;
}

export function siteCardStatus(site: Site, deploys: readonly SiteDeploy[] | undefined): CardStatus {
  const newest = deploys?.[0];
  if (newest?.status === 'PENDING') return { label: 'Deploying', tone: 'progress' };
  if (newest?.status === 'FAILED') return site.liveDeployId ? FAILED_LIVE_ON_PREVIOUS : { label: 'Failed', tone: 'error' };
  if (site.liveDeployId) return { label: 'Running', tone: 'success' };
  return { label: 'Not deployed', tone: 'neutral' };
}

type DatabaseDetailFields = Pick<DatabaseInstance, 'databaseType'> &
  Partial<Pick<DatabaseInstance, 'postgresVersion' | 'mysqlVersion' | 'documentDb'>>;

export function databaseCardDetail(project: DatabaseDetailFields, cpu: string | undefined): string {
  const engine = databaseEngine(project);
  const size = cpu ? ` · ${cpu} vCPU` : '';
  return `${engine}${size}`;
}

function databaseEngine(project: DatabaseDetailFields): string {
  if (project.documentDb === true) return 'DocumentDB';
  if (project.databaseType === 'MYSQL') return withVersion('MySQL', project.mysqlVersion);
  return withVersion('Postgres', project.postgresVersion);
}

const withVersion = (name: string, version?: string): string => (version ? `${name} ${version}` : name);

export const appCardDetail = (app: Pick<App, 'image' | 'internal'>): string =>
  `${app.internal ? 'Internal app' : 'App'} · ${app.image}`;

export const siteCardDetail = (site: Pick<Site, 'hostname'>): string => `Site · ${site.hostname}`;

export const bucketCardDetail = (bucket: { public: boolean }): string =>
  `Storage bucket · ${bucket.public ? 'public' : 'private'}`;

// The functions run on one shared runtime, so the card reports that runtime.
export function functionsCardStatus(runtime: RuntimeStatus | undefined): CardStatus | undefined {
  if (!runtime) return undefined;
  if (runtime.healthy) return { label: 'Running', tone: 'success' };
  if (runtime.status === 'restarting') return { label: 'Restarting', tone: 'progress' };
  return { label: `Runtime ${runtime.status}`, tone: 'error' };
}

export function functionsCardDetail(count: number): string {
  if (count === 0) return 'No functions yet';
  return count === 1 ? 'Edge function' : `${count} edge functions`;
}
