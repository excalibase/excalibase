import { vi } from 'vitest';
import { act, render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import type { ReactElement } from 'react';
import { api } from '../../../api/client';
import { useAuthStore } from '../../../stores/auth-store';
import type { DatabaseInstance } from '../../../types';

// Shared by the database panel tab tests: one fake control plane, the caller's
// org role, and the wait that lets every query finish before an absence is read.
export const database = (extra: Partial<DatabaseInstance> = {}) =>
  ({ projectId: 'p1', orgId: 'org-1', databaseType: 'POSTGRESQL', postgresVersion: '17', tier: 'FREE', status: 'ACTIVE', currentStage: 'COMPLETED', ...extra }) as DatabaseInstance;

export interface FakePlane {
  role: string;
  features: Record<string, boolean>;
  routes: Record<string, unknown>;
  failing: Set<string>;
}

export const plane: FakePlane = { role: 'owner', features: {}, routes: {}, failing: new Set() };

export function resetPlane() {
  plane.role = 'owner';
  plane.features = {};
  plane.routes = {};
  plane.failing = new Set();
  vi.clearAllMocks();
  useAuthStore.setState({ user: { id: '1', username: 'u', email: 'e', role: 'user' } } as never);
  vi.mocked(api.get).mockImplementation(((url: string) => answer(url)) as never);
  vi.mocked(api.post).mockResolvedValue({ data: { status: 'accepted' } } as never);
}

function answer(url: string): Promise<unknown> {
  const ok = (data: unknown) => Promise.resolve({ data });
  if (plane.failing.has(url)) return Promise.reject(Object.assign(new Error('x'), { response: { status: 500, data: { error: 'The server said no' } } }));
  if (url === '/orgs') return ok([{ id: 'org-1', name: 'Acme', role: plane.role }]);
  if (url === '/config') return ok({ deploymentMode: 'cloud', features: plane.features });
  if (url in plane.routes) return ok(plane.routes[url]);
  const prefix = Object.keys(plane.routes).find((key) => key.endsWith('*') && url.startsWith(key.slice(0, -1)));
  if (prefix) return ok(plane.routes[prefix]);
  return Promise.reject(Object.assign(new Error('not mocked'), { response: { status: 404, data: {} } }));
}

export function renderTab(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

// Lets every query in flight answer, so "is not there" reads the loaded page.
export const settle = () => act(async () => new Promise((resolve) => setTimeout(resolve, 150)));

export const calledWith = (fragment: string) => vi.mocked(api.get).mock.calls.some(([url]) => String(url).includes(fragment));
