import { beforeEach, describe, expect, test, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { api } from '../../../api/client';
import { UsersAnalyticsTab } from './UsersAnalyticsTab';
import { AbuseAnalyticsTab } from './AbuseAnalyticsTab';
import { TrafficAnalyticsTab } from './TrafficAnalyticsTab';
import { StorageAnalyticsTab } from './StorageAnalyticsTab';

const USERS_URL = '/admin/analytics/users';
const PEAK_LABEL = 'Peak DB connections';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn() } }));

const days = (n: number, fill: (i: number) => { total: number; flagged?: number }) =>
  Array.from({ length: n }, (_, i) => ({ day: `2026-11-${String(i + 1).padStart(2, '0')}`, ...fill(i) }));

const users = {
  windowDays: 30, signups: 200, suspended: 40, activeLast7Days: 90,
  perDay: days(30, (i) => (i === 29 ? { total: 91, flagged: 2 } : { total: 0 })),
  funnel: [{ label: 'Signed up', count: 200 }, { label: 'Verified email', count: 150 }, { label: 'Created a project', count: 100 }, { label: 'Never refused', count: 60 }],
  domains: [{ domain: 'fldubai.com', accounts: 29, suspended: 29 }, { domain: 'gmail.com', accounts: 146, suspended: 24 }, { domain: 'proton.me', accounts: 12, suspended: 1 }],
};
const abuse = {
  windowDays: 30, proxyRefusals: 140, registryRefusals: 9, refusedUsers: 99, refusedStillActive: 98, suspended: 96,
  perDay: days(30, (i) => (i === 28 ? { total: 78, flagged: 54 } : { total: 0 })),
  latest: [{ at: '2026-11-25T15:15:03Z', projectId: 'proj-7y14t3tmyi', userId: 'u1', email: 'x@example.test', kind: 'proxy', details: 'surface=create image="x"', suspended: false }],
};

function renderTab(ui: React.ReactElement, answers: Record<string, unknown>, features: Record<string, boolean> = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features } } as never);
    if (url in answers) return answers[url] instanceof Error ? Promise.reject(answers[url]) : Promise.resolve({ data: answers[url] } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>);
}

describe('Sign-ups tab', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows the four numbers, the funnel and the domains with their spam or mixed pill', async () => {
    renderTab(<UsersAnalyticsTab />, { [USERS_URL]: users });
    expect(await screen.findByText('Later suspended')).toBeInTheDocument();
    expect(screen.getByText('Sign-ups', { selector: 'dt' }).nextElementSibling).toHaveTextContent('200');
    expect(screen.getByText('Later suspended').nextElementSibling).toHaveTextContent('40');
    for (const step of ['Signed up', 'Verified email', 'Created a project']) expect(screen.getByText(step)).toBeInTheDocument();
    expect(screen.getByText('Never refused', { selector: 'span' })).toBeInTheDocument();
    const rows = screen.getAllByRole('row');
    expect(within(rows.find((row) => row.textContent?.includes('fldubai.com')) as HTMLElement).getByText('spam')).toBeInTheDocument();
    expect(within(rows.find((row) => row.textContent?.includes('gmail.com')) as HTMLElement).getByText('mixed')).toBeInTheDocument();
    expect(within(rows.find((row) => row.textContent?.includes('proton.me')) as HTMLElement).queryByText(/spam|mixed/)).toBeNull();
  });

  test('hovering the busiest day names its sign-ups and how many were suspended', async () => {
    renderTab(<UsersAnalyticsTab />, { [USERS_URL]: users });
    await userEvent.hover(await screen.findByTestId('bar-hit-29'));
    expect(screen.getByRole('tooltip')).toHaveTextContent('91 sign-ups, 2 suspended');
  });

  test('a window with no sign-ups says so instead of dividing by zero', async () => {
    renderTab(<UsersAnalyticsTab />, { [USERS_URL]: { ...users, signups: 0, suspended: 0, activeLast7Days: 0, perDay: days(30, () => ({ total: 0 })), funnel: users.funnel.map((s) => ({ ...s, count: 0 })), domains: [] } });
    expect(await screen.findByText('No sign-ups in the window.')).toBeInTheDocument();
    expect(screen.queryByText(/NaN/)).toBeNull();
  });

  test('a failed read shows an alert, not an empty tab', async () => {
    renderTab(<UsersAnalyticsTab />, { [USERS_URL]: new Error('boom') });
    expect(await screen.findByRole('alert')).toBeInTheDocument();
  });
});

describe('Abuse tab', () => {
  beforeEach(() => vi.clearAllMocks());

  test('puts the refused-but-active count first and lists the latest refusals', async () => {
    renderTab(<AbuseAnalyticsTab />, { '/admin/analytics/abuse': abuse });
    expect(await screen.findByText('98 accounts with refused deploys are still active.')).toBeInTheDocument();
    expect(screen.getByText('proj-7y14t3tmyi')).toBeInTheDocument();
    expect(screen.getByText('x@example.test')).toBeInTheDocument();
    await userEvent.hover(screen.getByTestId('bar-hit-28'));
    expect(screen.getByRole('tooltip')).toHaveTextContent('78 refusals, 54 users');
  });

  test('with nobody to act on there is no warning and the empty list says so', async () => {
    renderTab(<AbuseAnalyticsTab />, { '/admin/analytics/abuse': { ...abuse, refusedStillActive: 0, refusedUsers: 0, latest: [] } });
    expect(await screen.findByText('No refused deploys in the window.')).toBeInTheDocument();
    expect(screen.queryByRole('note')).toBeNull();
  });
});

describe('Traffic tab', () => {
  beforeEach(() => vi.clearAllMocks());
  const traffic = { windowDays: 7, peakConnections: 35, peakConnectionsPerDay: days(7, (i) => ({ total: i === 3 ? 35 : 0 })) };

  test('dashes for what is not collected, the one real number, and a plain notice', async () => {
    renderTab(<TrafficAnalyticsTab />, { '/admin/analytics/traffic': traffic });
    expect(await screen.findByText(PEAK_LABEL)).toBeInTheDocument();
    expect(screen.getByText(PEAK_LABEL).nextElementSibling).toHaveTextContent('35');
    for (const label of ['API requests', 'Egress', 'DB queries/s']) expect(screen.getByText(label).nextElementSibling).toHaveTextContent('—');
    expect(screen.getByRole('note')).toHaveTextContent('not collected yet');
    expect(screen.getByRole('note')).toHaveTextContent('off on this install');
    expect(screen.getByText('Peak database connections per day, last 7 days')).toBeInTheDocument();
    expect(screen.getAllByTestId(/^bar-hit-/)).toHaveLength(7);
  });

  test('the notice follows the request-metrics flag instead of claiming it is off', async () => {
    renderTab(<TrafficAnalyticsTab />, { '/admin/analytics/traffic': traffic }, { requestmetrics: true });
    await screen.findByText(PEAK_LABEL);
    expect(await screen.findByText(/on here/)).toBeInTheDocument();
  });
});

describe('Storage tab', () => {
  test('asks the server for nothing and shows only dashes and notices', () => {
    vi.clearAllMocks();
    render(<StorageAnalyticsTab />);
    expect(api.get).not.toHaveBeenCalled();
    expect(screen.getAllByText('—')).toHaveLength(4);
    expect(screen.getAllByRole('note')).toHaveLength(2);
  });
});
