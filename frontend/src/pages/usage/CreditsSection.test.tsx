import { describe, test, expect, vi, beforeEach, afterEach } from 'vitest';
import { screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { CreditsSection } from './CreditsSection';
import { api } from '../../api/client';
import { formatInstant } from '../../utils/instant';
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

  describe('burn', () => {
    const week = ['2026-11-25', '2026-11-26', '2026-11-27', '2026-11-28', '2026-11-29', '2026-11-30', '2026-12-01'];
    const burning = (extra: object = {}) => creditOverview({
      balanceMicros: 40_000_000,
      burn: { perDayMicros: 4_000_000, runOutAt: '2026-12-11T00:00:00Z', days: week.map((day, i) => ({ day, micros: i === 6 ? 5_000_000 : 0 })) },
      ...extra,
    });

    test('shows what the org burns a day and the day the credit runs out', async () => {
      mountSection(burning());
      const section = within(await screen.findByRole('region', { name: 'Credits' }));
      expect(section.getByText('Burn per day').nextSibling).toHaveTextContent('$4.00');
      const runOut = section.getByText('Projected run-out').nextSibling;
      expect(runOut).toHaveTextContent(formatInstant('2026-12-11T00:00:00Z', 'date'));
      expect(runOut).toHaveTextContent('10 days');
    });

    test('lists the last seven days, each with its own amount', async () => {
      mountSection(burning());
      const days = within(await screen.findByRole('list', { name: 'Burn over the last 7 days' })).getAllByRole('listitem');
      expect(days).toHaveLength(7);
      expect(days[6]).toHaveTextContent('2026-12-01');
      expect(days[6]).toHaveTextContent('$5.00');
      expect(days[0]).toHaveTextContent('$0.00');
    });

    test('says when the credit expires before it would run out', async () => {
      mountSection(burning({ nextExpiry: '2026-12-05T00:00:00Z' }));
      expect(await screen.findByText(/expires before it runs out/)).toBeInTheDocument();
    });

    test('says the credit is used up at a zero balance, whatever the burn', async () => {
      mountSection(burning({ balanceMicros: 0, lots: [], burn: { perDayMicros: 4_000_000, runOutAt: null, days: [] } }));
      const section = within(await screen.findByRole('region', { name: 'Credits' }));
      expect(section.getByText('Projected run-out').nextSibling).toHaveTextContent('Credit used up');
      expect(section.queryByText('No usage yet')).toBeNull();
    });

    test('shows no date, and never "No usage yet", when the run-out is too far away to name', async () => {
      mountSection(burning({ burn: { perDayMicros: 1, runOutAt: null, days: [] } }));
      const section = within(await screen.findByRole('region', { name: 'Credits' }));
      expect(section.getByText('Projected run-out').nextSibling).toHaveTextContent('More than 10 years away');
      expect(section.queryByText('No usage yet')).toBeNull();
    });

    test('a usage line the credit could not pay in full shows what was not covered', async () => {
      const usage = { id: 9, kind: 'usage', amountMicros: -1_000_000, actor: 'system', reason: '', periodStart: '2026-11-30T10:00:00Z', createdAt: '2026-11-30T11:10:00Z', details: { uncovered_micros: 3_500_000 } };
      mountSection(burning({ entries: [usage] }));
      expect(await screen.findByText(/Not covered: \$3\.50/)).toBeInTheDocument();
    });

    test('says nothing about a run-out when nothing has been used', async () => {
      mountSection(creditOverview());
      const section = within(await screen.findByRole('region', { name: 'Credits' }));
      expect(section.getByText('Burn per day').nextSibling).toHaveTextContent('$0.00');
      expect(section.getByText('Projected run-out').nextSibling).toHaveTextContent('No usage yet');
    });

    test('says usage is not billed yet while metering is off, and shows no burn', async () => {
      mountSection(creditOverview({ metering: false }));
      await screen.findByRole('region', { name: 'Credits' });
      expect(screen.getByText(/Usage is not billed yet/)).toBeInTheDocument();
      expect(screen.queryByText('Burn per day')).toBeNull();
    });
  });
});
