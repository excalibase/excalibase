import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { IconRail } from './IconRail';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn() },
}));

function renderRail(config: Record<string, unknown> | Error, onSectionClick = vi.fn()) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') {
      return config instanceof Error
        ? Promise.reject(config)
        : Promise.resolve({ data: config } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1']}>
        <Routes>
          <Route
            path="/project/:projectId"
            element={<IconRail activeSection={null} onSectionClick={onSectionClick} />}
          />
          <Route path="/project/:projectId/*" element={<div data-testid="routed">routed</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('IconRail services', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
  });

  test('shows Databases and Containers as peers when the server has app hosting on', async () => {
    renderRail({ deploymentMode: 'cloud', appHosting: true });
    expect(await screen.findByTestId('nav-containers')).toBeInTheDocument();
    expect(screen.getByTestId('nav-database')).toHaveAttribute('title', 'Databases');
  });

  test('hides Containers when the server has app hosting off', async () => {
    renderRail({ deploymentMode: 'cloud', appHosting: false });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.getByTestId('nav-database')).toBeInTheDocument();
    expect(screen.queryByTestId('nav-containers')).not.toBeInTheDocument();
  });

  test('hides Containers when the server does not report the capability', async () => {
    renderRail(new Error('network down'));
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByTestId('nav-containers')).not.toBeInTheDocument();
  });

  test('opens a section with children on its first page', async () => {
    const onSectionClick = vi.fn();
    renderRail({ deploymentMode: 'cloud', appHosting: true }, onSectionClick);
    fireEvent.click(screen.getByTestId('nav-database'));
    expect(onSectionClick).toHaveBeenCalledWith('database');
    expect(await screen.findByTestId('routed')).toBeInTheDocument();
  });

  test('navigates to Containers when it is clicked', async () => {
    const onSectionClick = vi.fn();
    renderRail({ deploymentMode: 'cloud', appHosting: true }, onSectionClick);
    fireEvent.click(await screen.findByTestId('nav-containers'));
    expect(onSectionClick).toHaveBeenCalledWith('');
    expect(await screen.findByTestId('routed')).toBeInTheDocument();
  });

  test('remembers the chosen sidebar behaviour', () => {
    renderRail({ deploymentMode: 'cloud', appHosting: false });
    fireEvent.mouseEnter(screen.getByTestId('icon-rail'));
    fireEvent.click(screen.getByTestId('sidebar-settings'));
    fireEvent.click(screen.getByText('Expanded'));
    expect(localStorage.getItem('sidebar-behavior')).toBe('expanded');
    fireEvent.mouseLeave(screen.getByTestId('icon-rail'));
    expect(screen.getByText('Databases')).toBeInTheDocument();
  });
});
