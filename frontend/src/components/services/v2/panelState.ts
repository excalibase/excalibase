// The selected service and its tab live in the URL (?service=app:api&tab=logs),
// so a panel can be linked to and the back button steps through selections.

export type ServiceKind = 'database' | 'app' | 'site' | 'storage' | 'functions';

export interface ServiceRef {
  readonly kind: ServiceKind;
  readonly id?: string;
}

export interface PanelState {
  readonly service: ServiceRef | null;
  readonly tab: string;
}

export interface PanelTab {
  readonly key: string;
  readonly label: string;
}

const tab = (key: string, label: string): PanelTab => ({ key, label });

const TABS: Readonly<Record<ServiceKind, readonly PanelTab[]>> = {
  database: [
    tab('overview', 'Overview'),
    tab('connect', 'Connect'),
    tab('data', 'Data'),
    tab('backups', 'Backups'),
    tab('metrics', 'Metrics'),
    tab('logs', 'Logs'),
    tab('settings', 'Settings'),
  ],
  app: [
    tab('overview', 'Overview'),
    tab('deploys', 'Deploys'),
    tab('variables', 'Variables'),
    tab('metrics', 'Metrics'),
    tab('logs', 'Logs'),
    tab('settings', 'Settings'),
  ],
  site: [tab('overview', 'Overview'), tab('deploys', 'Deploys'), tab('variables', 'Variables'), tab('settings', 'Settings')],
  storage: [],
  functions: [],
};

export const tabsFor = (kind: ServiceKind): readonly PanelTab[] => TABS[kind];

const NEEDS_ID: ReadonlySet<ServiceKind> = new Set(['app', 'site', 'storage']);
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

export function serviceKey(ref: ServiceRef): string {
  return ref.id ? `${ref.kind}:${ref.id}` : ref.kind;
}

export function parseServiceKey(key: string): ServiceRef | null {
  const [kind, id, ...extra] = key.split(':');
  if (extra.length > 0 || !Object.hasOwn(TABS, kind)) return null;
  const serviceKind = kind as ServiceKind;
  if (NEEDS_ID.has(serviceKind)) return id !== undefined && SAFE_ID.test(id) ? { kind: serviceKind, id } : null;
  return id === undefined ? { kind: serviceKind } : null;
}

export function parsePanelState(params: URLSearchParams): PanelState {
  const service = parseServiceKey(params.get('service') ?? '');
  if (!service) return { service: null, tab: '' };
  const tabs = tabsFor(service.kind);
  if (tabs.length === 0) return { service, tab: '' };
  const asked = params.get('tab');
  return { service, tab: tabs.find((t) => t.key === asked)?.key ?? tabs[0].key };
}

// The next query string: other parameters stay, the panel's two are replaced.
export function panelSearch(current: URLSearchParams, service: ServiceRef | null, tabKey?: string): URLSearchParams {
  const next = new URLSearchParams(current);
  next.delete('service');
  next.delete('tab');
  if (service) {
    next.set('service', serviceKey(service));
    if (tabKey) next.set('tab', tabKey);
  }
  return next;
}
