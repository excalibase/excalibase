import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { AlertsPage } from './AlertsPage';
import { api } from '../api/client';
import { formatInstant } from '../utils/instant';

vi.mock('../hooks/useRouteProjectId', () => ({ useRouteProjectId: () => 'proj-1' }));
vi.mock('../api/client', () => ({ api: { get: vi.fn() } }));

const sent = JSON.parse(readFileSync(resolve(process.cwd(), '../shared-fixtures/wire/alert.json'), 'utf8'));

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><AlertsPage /></QueryClientProvider>);
}

// EXC-893: the page read firedAt, metricName and status, none of which the server sends.
describe('AlertsPage shows what the server sends', () => {
  test('the time, the metric and an open alert as Firing', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [sent] } as never);
    renderPage();
    expect(await screen.findByText(formatInstant(sent.timestamp))).toBeInTheDocument();
    expect(screen.getByText('connections')).toBeInTheDocument();
    expect(screen.getByText('Firing')).toBeInTheDocument();
  });

  test('a resolved alert reads Resolved', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [{ ...sent, resolved: true }] } as never);
    renderPage();
    expect(await screen.findByText('Resolved')).toBeInTheDocument();
  });
});
