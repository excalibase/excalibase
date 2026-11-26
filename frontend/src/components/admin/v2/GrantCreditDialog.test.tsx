import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { GrantCreditDialog } from './GrantCreditDialog';
import { api } from '../../../api/client';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const setup = () => {
  const onDone = vi.fn();
  render(<QueryClientProvider client={new QueryClient()}><GrantCreditDialog orgId="org-9" orgSlug="acme" onDone={onDone} onCancel={vi.fn()} /></QueryClientProvider>);
  return onDone;
};
const fill = async (amount: string, reason: string) => {
  await userEvent.type(screen.getByLabelText('Amount in dollars'), amount);
  await userEvent.type(screen.getByLabelText('Reason'), reason);
};

beforeEach(() => vi.clearAllMocks());

describe('Grant credit dialog', () => {
  test('sends micros, the reason and an idempotency key', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} });
    const onDone = setup();
    await fill('25.5', 'goodwill after the outage');
    await userEvent.click(screen.getByRole('button', { name: 'Add credit' }));
    await waitFor(() => expect(onDone).toHaveBeenCalled());
    const [url, body, config] = vi.mocked(api.post).mock.calls[0];
    expect(url).toBe('/admin/console/orgs/org-9/credits');
    expect(body).toMatchObject({ amountMicros: 25_500_000, reason: 'goodwill after the outage' });
    expect((config as { headers: Record<string, string> }).headers['Idempotency-Key']).toMatch(/^grant-/);
  });

  test('taking credit back sends a negative amount', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} });
    setup();
    await userEvent.selectOptions(screen.getByLabelText('Action'), 'take');
    await fill('10', 'claw back after a refund');
    await userEvent.click(screen.getByRole('button', { name: 'Take back credit' }));
    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({ amountMicros: -10_000_000 });
  });

  test('refuses a bad amount and one above the cap before asking the server', async () => {
    setup();
    await fill('abc', 'a reason that is long enough');
    expect(screen.getByRole('alert')).toHaveTextContent(/Enter an amount/);
    expect(screen.getByRole('button', { name: 'Add credit' })).toBeDisabled();
    await userEvent.clear(screen.getByLabelText('Amount in dollars'));
    await userEvent.type(screen.getByLabelText('Amount in dollars'), '1000.01');
    expect(screen.getByRole('alert')).toHaveTextContent(/at most \$1,000.00/);
    expect(screen.getByRole('button', { name: 'Add credit' })).toBeDisabled();
    expect(api.post).not.toHaveBeenCalled();
  });

  test('a short reason keeps the button off', async () => {
    setup();
    await fill('5', 'short');
    expect(screen.getByRole('button', { name: 'Add credit' })).toBeDisabled();
  });
});
