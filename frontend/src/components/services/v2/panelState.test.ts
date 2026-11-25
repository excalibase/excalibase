import { describe, test, expect } from 'vitest';
import { parsePanelState, panelSearch, serviceKey, tabsFor, parseServiceKey } from './panelState';

const params = (query: string) => new URLSearchParams(query);

describe('serviceKey', () => {
  test('names each service so it fits in a URL', () => {
    expect(serviceKey({ kind: 'database' })).toBe('database');
    expect(serviceKey({ kind: 'app', id: 'api' })).toBe('app:api');
    expect(serviceKey({ kind: 'site', id: 'web' })).toBe('site:web');
    expect(serviceKey({ kind: 'storage', id: 'avatars' })).toBe('storage:avatars');
    expect(serviceKey({ kind: 'functions' })).toBe('functions');
  });

  test('round-trips through parseServiceKey', () => {
    for (const ref of [{ kind: 'database' }, { kind: 'app', id: 'a1' }, { kind: 'site', id: 's-2' }, { kind: 'functions' }] as const) {
      expect(parseServiceKey(serviceKey(ref))).toEqual(ref);
    }
  });

  test.each(['', 'nope', 'app', 'app:', 'app:../x', 'site:a b', 'database:x', 'functions:x', 'app:<script>'])(
    'refuses %j',
    (bad) => {
      expect(parseServiceKey(bad)).toBeNull();
    },
  );
});

describe('tabsFor', () => {
  test('a database has seven tabs, in the owner-approved order', () => {
    expect(tabsFor('database').map((t) => t.key)).toEqual(['overview', 'connect', 'data', 'backups', 'metrics', 'logs', 'settings']);
  });
  test('an app has six tabs', () => {
    expect(tabsFor('app').map((t) => t.key)).toEqual(['overview', 'deploys', 'variables', 'metrics', 'logs', 'settings']);
  });
  test('a site has four tabs', () => {
    expect(tabsFor('site').map((t) => t.key)).toEqual(['overview', 'deploys', 'variables', 'settings']);
  });
  test('functions and storage show their own view with no tabs', () => {
    expect(tabsFor('functions')).toEqual([]);
    expect(tabsFor('storage')).toEqual([]);
  });
});

describe('parsePanelState', () => {
  test('no service in the URL means no panel', () => {
    expect(parsePanelState(params(''))).toEqual({ service: null, tab: '' });
  });

  test('reads the service and its tab', () => {
    expect(parsePanelState(params('service=database&tab=backups'))).toEqual({ service: { kind: 'database' }, tab: 'backups' });
    expect(parsePanelState(params('service=app:api&tab=logs'))).toEqual({ service: { kind: 'app', id: 'api' }, tab: 'logs' });
  });

  test('a missing tab opens the first tab', () => {
    expect(parsePanelState(params('service=site:web'))).toEqual({ service: { kind: 'site', id: 'web' }, tab: 'overview' });
  });

  test('a tab the service does not have opens the first tab', () => {
    expect(parsePanelState(params('service=site:web&tab=metrics')).tab).toBe('overview');
    expect(parsePanelState(params('service=database&tab=deploys')).tab).toBe('overview');
  });

  test('a service without tabs carries none', () => {
    expect(parsePanelState(params('service=functions&tab=data')).tab).toBe('');
  });

  test('an unreadable service is no panel, not a guess', () => {
    expect(parsePanelState(params('service=wat&tab=logs'))).toEqual({ service: null, tab: '' });
  });
});

describe('panelSearch', () => {
  test('selecting a service drops any old tab', () => {
    const next = panelSearch(params('service=database&tab=logs'), { kind: 'app', id: 'api' });
    expect(next.toString()).toBe('service=app%3Aapi');
  });

  test('changing the tab keeps the service', () => {
    const next = panelSearch(params('service=database'), { kind: 'database' }, 'connect');
    expect(next.get('service')).toBe('database');
    expect(next.get('tab')).toBe('connect');
  });

  test('closing the panel clears both', () => {
    expect(panelSearch(params('service=database&tab=logs'), null).toString()).toBe('');
  });

  test('other query parameters survive', () => {
    expect(panelSearch(params('x=1'), { kind: 'functions' }).get('x')).toBe('1');
  });

  test('does not change the params it was given', () => {
    const original = params('service=database');
    panelSearch(original, null);
    expect(original.get('service')).toBe('database');
  });
});
