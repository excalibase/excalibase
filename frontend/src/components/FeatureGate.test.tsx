import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { FeatureGate } from './FeatureGate';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn() },
}));

function renderGate(config: Record<string, unknown>) {
  vi.mocked(api.get).mockResolvedValue({ data: config } as never);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <FeatureGate feature="mcp">
        <p>AI tools page</p>
      </FeatureGate>
    </QueryClientProvider>,
  );
}

describe('FeatureGate', () => {
  beforeEach(() => vi.mocked(api.get).mockReset());

  test('shows the page when the server has the feature on', async () => {
    renderGate({ deploymentMode: 'cloud', features: { mcp: true, pipeline: false } });
    expect(await screen.findByText('AI tools page')).toBeInTheDocument();
  });

  test('shows not available when the feature is off', async () => {
    renderGate({ deploymentMode: 'cloud', features: { mcp: false, pipeline: true } });
    expect(await screen.findByTestId('feature-unavailable')).toBeInTheDocument();
    expect(screen.queryByText('AI tools page')).not.toBeInTheDocument();
  });

  test('a server that reports no features has them all off', async () => {
    renderGate({ deploymentMode: 'cloud' });
    expect(await screen.findByTestId('feature-unavailable')).toBeInTheDocument();
  });
});
