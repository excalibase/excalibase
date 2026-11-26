import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { UsagePage } from './UsagePage';
import { api } from '../api/client';
import { creditOverview, mountWith, project } from '../test/homeFixtures';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const FREE = { tier: 'FREE', version: 0, maxProjects: 2, instances: 1, storageSize: '5Gi', maxStorageSize: '5Gi', maxApps: 2, cpu: '0.5', memory: '512Mi' };
const ON = { credits: true };

beforeEach(() => vi.clearAllMocks());

describe('Usage & credits with the credits feature on', () => {
  test('shows the balance, the expiry, the history and the UTC note', async () => {
    mountWith({ projects: [project('p1')], tiers: [FREE], features: ON, credits: creditOverview() }, <UsagePage />);
    const section = within(await screen.findByRole('region', { name: 'Credits' }));
    expect(section.getByText('Balance').nextSibling).toHaveTextContent('$300.00');
    expect(section.getByText(/Next credit expires/)).toBeInTheDocument();
    expect(section.getByText(/Sign-up credit · sign-up credit/)).toBeInTheDocument();
    expect(section.getByText(/in UTC/)).toBeInTheDocument();
    expect(section.getByRole('link', { name: 'Download CSV' })).toHaveAttribute('href', expect.stringContaining('/orgs/org-1/credits/statement?month='));
    expect(section.getByRole('link', { name: 'Download CSV' }).getAttribute('href')).toContain('format=csv');
  });

  test('offers the sign-up credit behind the terms checkbox', async () => {
    const overview = creditOverview({ balanceMicros: 0, lots: [], entries: [], nextExpiry: null, trial: { state: 'needs_terms', amountMicros: 300_000_000, days: 90 } });
    mountWith({ projects: [project('p1')], tiers: [FREE], features: ON, credits: overview }, <UsagePage />);
    const accept = await screen.findByRole('button', { name: 'Accept and get $300.00' });
    expect(accept).toBeDisabled();
    await userEvent.click(screen.getByRole('checkbox'));
    expect(accept).toBeEnabled();
    vi.mocked(api.post).mockResolvedValue({ data: { accepted: true, outcome: 'granted' } });
    await userEvent.click(accept);
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/credits/terms', { version: 'v1' }));
  });

  test('says where a waitlisted person stands', async () => {
    const overview = creditOverview({ balanceMicros: 0, lots: [], entries: [], nextExpiry: null, trial: { state: 'waitlisted', position: 7, amountMicros: 300_000_000, days: 90 } });
    mountWith({ projects: [project('p1')], tiers: [FREE], features: ON, credits: overview }, <UsagePage />);
    expect(await screen.findByText(/number 7 in line/)).toBeInTheDocument();
  });

  test('a failed credit read says so and keeps the plan', async () => {
    mountWith({ projects: [project('p1')], tiers: [FREE], features: ON }, <UsagePage />);
    expect(await screen.findByText(/Could not load your credit/)).toBeInTheDocument();
    expect(await screen.findByRole('region', { name: 'Plan' })).toBeInTheDocument();
  });

  test('with the feature off the page is today\'s page and asks nothing', async () => {
    mountWith({ projects: [project('p1')], tiers: [FREE], features: { credits: false }, credits: creditOverview() }, <UsagePage />);
    await screen.findByRole('region', { name: 'Plan' });
    expect(screen.queryByRole('region', { name: 'Credits' })).not.toBeInTheDocument();
    expect(vi.mocked(api.get).mock.calls.some(([url]) => String(url).includes('/credits'))).toBe(false);
  });
});
