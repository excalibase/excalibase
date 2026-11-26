import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { CreditsSection } from './CreditsSection';
import { api } from '../../api/client';
import { creditOverview, mountWith, project } from '../../test/homeFixtures';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const mountSection = (credits?: object) => mountWith({ projects: [project('p1')], features: { credits: true }, credits }, <CreditsSection orgId="org-1" />);
const offer = (state: object) => creditOverview({ balanceMicros: 0, lots: [], entries: [], nextExpiry: null, trial: { amountMicros: 300_000_000, days: 90, ...state } });

describe('CreditsSection', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ toFake: ['Date'], now: new Date('2026-12-01T00:00:00Z') });
  });
  afterEach(() => vi.useRealTimers());

  test('the accept button stays off until the terms are ticked, then sends the version it was shown', async () => {
    mountSection(offer({ state: 'needs_terms' }));
    const accept = await screen.findByRole('button', { name: 'Accept and get $300.00' });
    expect(accept).toBeDisabled();
    expect(screen.getByRole('link', { name: /terms/i })).toHaveAttribute('href', 'https://example.test/terms');
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(screen.getByRole('checkbox'));
    expect(accept).toBeEnabled();
    vi.mocked(api.post).mockResolvedValue({ data: { accepted: true, outcome: 'granted' } });
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).click(accept);
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/credits/terms', { version: 'v1' }));
  });

  test('a granted org sees no offer', async () => {
    mountSection(creditOverview());
    await screen.findByRole('region', { name: 'Credits' });
    expect(screen.queryByRole('checkbox')).toBeNull();
  });

  test('a waitlisted person is told their place, and nothing to accept', async () => {
    mountSection(offer({ state: 'waitlisted', position: 7 }));
    expect(await screen.findByText(/number 7 in line/)).toBeInTheDocument();
    expect(screen.queryByRole('checkbox')).toBeNull();
  });

  test('the expiry line counts the days left', async () => {
    mountSection(creditOverview({ nextExpiry: '2027-02-25T00:00:00Z' }));
    expect(await screen.findByText(/86 days left/)).toBeInTheDocument();
  });

  test('each lot gets its own line once there is more than one, with the date it ends or that it does not', async () => {
    const lot = (id: number, expiresAt: string | null, remainingMicros: number) => ({ id, kind: 'grant_admin', originalMicros: remainingMicros, remainingMicros, expiresAt, createdAt: '2026-11-27T00:00:00Z' });
    mountSection(creditOverview({ lots: [lot(1, '2027-01-10T00:00:00Z', 100_000_000), lot(2, null, 50_000_000)] }));
    const section = within(await screen.findByRole('region', { name: 'Credits' }));
    expect(section.getByText('Does not expire').nextSibling).toHaveTextContent('$50.00');
    expect(section.getByText(/^Expires /).nextSibling).toHaveTextContent('$100.00');
  });

  test('the statement link follows the month chosen', async () => {
    mountSection(creditOverview({ entries: [] }));
    const section = within(await screen.findByRole('region', { name: 'Credits' }));
    expect(section.getByRole('link', { name: 'Download CSV' }).getAttribute('href')).toMatch(/\/orgs\/org-1\/credits\/statement\?month=2026-12&format=csv/);
    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime }).selectOptions(section.getByLabelText('Statement for'), '2026-11');
    expect(section.getByRole('link', { name: 'Download CSV' }).getAttribute('href')).toContain('month=2026-11');
  });

  test('a failed read says so', async () => {
    mountSection(undefined);
    expect(await screen.findByText(/Could not load your credit/)).toBeInTheDocument();
  });
});
