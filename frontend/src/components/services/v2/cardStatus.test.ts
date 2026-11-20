import { describe, test, expect } from 'vitest';
import type { App, Deploy } from '../../../api/apps';
import type { Site, SiteDeploy } from '../../../api/sites';
import { appCardStatus, siteCardStatus, databaseCardDetail, appCardDetail, siteCardDetail, bucketCardDetail } from './cardStatus';

const app = (extra: Partial<App> = {}) => ({ id: 'a', name: 'api', image: 'ghcr.io/me/api:1.4', status: 'ACTIVE', replicas: 1, env: [], ...extra }) as App;
const deploy = (status: Deploy['status'], extra: Partial<Deploy> = {}) => ({ id: 'd', status, revision: 1, ...extra }) as Deploy;

describe('appCardStatus', () => {
  test('a succeeded newest deploy is running', () => {
    expect(appCardStatus(app(), [deploy('succeeded')])).toEqual({ label: 'Running', tone: 'success' });
  });
  test('a failed newest deploy with an earlier live one says the previous one is live', () => {
    expect(appCardStatus(app(), [deploy('failed', { revision: 3 }), deploy('succeeded', { revision: 2 })])).toEqual({
      label: 'Last deploy failed · live on previous',
      tone: 'warning',
    });
  });
  test('a failed first deploy is simply failed', () => {
    expect(appCardStatus(app(), [deploy('failed')])).toEqual({ label: 'Failed', tone: 'error' });
  });
  test('a paused app is paused whatever its deploys say', () => {
    expect(appCardStatus(app({ status: 'PAUSED' }), [deploy('failed'), deploy('succeeded')])).toEqual({ label: 'Paused', tone: 'warning' });
  });
  test('a deploy in flight is deploying', () => {
    expect(appCardStatus(app(), [deploy('rolling'), deploy('succeeded')])).toEqual({ label: 'Deploying', tone: 'progress' });
  });
  test('no deploy yet is not deployed', () => {
    expect(appCardStatus(app(), [])).toEqual({ label: 'Not deployed', tone: 'neutral' });
  });
  test('deploys not yet read leave the app status alone', () => {
    expect(appCardStatus(app(), undefined)).toEqual({ label: 'Not deployed', tone: 'neutral' });
  });
});

const site = (extra: Partial<Site> = {}) => ({ id: 's', name: 'web', hostname: 'web.excalibase.app', url: 'https://web.excalibase.app', ...extra }) as Site;
const siteDeploy = (status: SiteDeploy['status']) => ({ id: 'x', status }) as SiteDeploy;

describe('siteCardStatus', () => {
  test('a live site is running', () => {
    expect(siteCardStatus(site({ liveDeployId: 'd1' }), [siteDeploy('SUCCEEDED')])).toEqual({ label: 'Running', tone: 'success' });
  });
  test('a failed newest deploy while another is live', () => {
    expect(siteCardStatus(site({ liveDeployId: 'd1' }), [siteDeploy('FAILED'), siteDeploy('SUCCEEDED')])).toEqual({
      label: 'Last deploy failed · live on previous',
      tone: 'warning',
    });
  });
  test('a failed deploy with nothing live is failed', () => {
    expect(siteCardStatus(site(), [siteDeploy('FAILED')])).toEqual({ label: 'Failed', tone: 'error' });
  });
  test('nothing deployed', () => {
    expect(siteCardStatus(site(), [])).toEqual({ label: 'Not deployed', tone: 'neutral' });
  });
  test('a pending deploy is deploying', () => {
    expect(siteCardStatus(site(), [siteDeploy('PENDING')])).toEqual({ label: 'Deploying', tone: 'progress' });
  });
});

describe('card details', () => {
  test('database: engine, version and size', () => {
    expect(databaseCardDetail({ databaseType: 'POSTGRESQL', postgresVersion: '16' }, '1')).toBe('Postgres 16 · 1 vCPU');
    expect(databaseCardDetail({ databaseType: 'POSTGRESQL', postgresVersion: '17' }, '0.5')).toBe('Postgres 17 · 0.5 vCPU');
    expect(databaseCardDetail({ databaseType: 'MYSQL', mysqlVersion: '8.4.10' }, undefined)).toBe('MySQL 8.4.10');
    expect(databaseCardDetail({ databaseType: 'POSTGRESQL', documentDb: true }, '2')).toBe('DocumentDB · 2 vCPU');
  });
  test('app: image', () => {
    expect(appCardDetail(app())).toBe('App · ghcr.io/me/api:1.4');
    expect(appCardDetail(app({ internal: true }))).toBe('Internal app · ghcr.io/me/api:1.4');
  });
  test('site: domain', () => {
    expect(siteCardDetail(site())).toBe('Site · web.excalibase.app');
  });
  test('bucket: privacy', () => {
    expect(bucketCardDetail({ public: false })).toBe('Storage bucket · private');
    expect(bucketCardDetail({ public: true })).toBe('Storage bucket · public');
  });
});
