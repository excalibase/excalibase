import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { ProviderButtons, ProviderRefusal } from './ProviderSignIn';
import { api } from '../../api/client';
import { API_BASE } from '../../api/base';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn() },
}));

describe('provider sign-in buttons', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test('links straight to the provider without an invite', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { providers: ['google'] } } as never);
    render(<ProviderButtons />);
    expect(await screen.findByRole('link', { name: 'Continue with Google' }))
      .toHaveAttribute('href', `${API_BASE}/auth/oauth/google/start`);
  });

  test('a provider without a display name is offered by its id', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { providers: ['gitlab'] } } as never);
    render(<ProviderButtons />);
    expect(await screen.findByRole('link', { name: 'Continue with gitlab' })).toBeInTheDocument();
  });

  test('offers nothing when the provider list cannot be read', async () => {
    const cases = [() => Promise.reject(new Error('network')), () => Promise.resolve({ data: {} })];
    for (const respond of cases) {
      vi.mocked(api.get).mockImplementation(respond as never);
      const { container, unmount } = render(<ProviderButtons />);
      await waitFor(() => expect(api.get).toHaveBeenCalledWith('/auth/oauth/providers'));
      expect(container).toBeEmptyDOMElement();
      unmount();
    }
  });
});

describe('provider sign-in refusals', () => {
  test('says nothing without a reason', () => {
    const { container } = render(<ProviderRefusal reason={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  test('an unknown reason reads as a failed sign-in', () => {
    render(<ProviderRefusal reason="something-new" />);
    expect(screen.getByText('Sign-in failed. Please try again.')).toBeInTheDocument();
  });
});
