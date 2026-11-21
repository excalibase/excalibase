import { vi } from 'vitest';
import { render } from '@testing-library/react';
import { createElement, type ReactElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';
import { useOrgSelection } from '../stores/org-selection';

export const ORGS = [
  { id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1', role: 'owner' },
  { id: 'org-other', name: 'Not Mine', slug: 'nm', tier: 'FREE', ownerId: 'u9' },
];

export function project(projectId: string, extra: object = {}) {
  return { projectId, orgId: 'org-1', databaseType: 'POSTGRESQL', tier: 'FREE', namespace: 'ns', status: 'ACTIVE', currentStage: 'COMPLETED', createdAt: '2026-10-01T00:00:00Z', ...extra };
}

export interface Backend {
  projects: unknown;
  failProjects?: boolean;
  tokens?: unknown[];
  apps?: Record<string, unknown[]>;
  sites?: Record<string, unknown[]>;
  allowed?: Record<string, unknown>;
  features?: Record<string, boolean>;
}

// Answers the Studio's GETs from `backend`; anything else is an empty list.
export function mountWith(backend: Backend, ui: ReactElement, route = '/') {
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'dev@example.test', role: 'user' }, isAuthenticated: true });
  useOrgSelection.setState({ orgId: null });
  vi.mocked(api.get).mockImplementation(((url: string) => {
    const reply = (data: unknown) => Promise.resolve({ data });
    if (url === '/config') return reply({ deploymentMode: 'cloud', appHosting: true, sites: true, features: { studio_v2: true, dballowlist: true, mcp: true, ...backend.features } });
    if (url === '/orgs') return reply(ORGS);
    if (url === '/provision') return backend.failProjects ? Promise.reject(Object.assign(new Error('forbidden'), { response: { status: 403 } })) : reply(backend.projects);
    if (url === '/auth/tokens') return reply(backend.tokens ?? []);
    const scoped = /^\/projects\/([^/]+)\/(.*?)\/?$/.exec(url);
    if (scoped?.[2] === 'apps') return reply(backend.apps?.[scoped[1]] ?? []);
    if (scoped?.[2] === 'sites') return reply(backend.sites?.[scoped[1]] ?? []);
    if (scoped?.[2] === 'db-endpoint/allowed-sources') return reply(backend.allowed?.[scoped[1]] ?? { access: 'listed', sources: [], publicEnabled: false });
    return reply([]);
  }) as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(createElement(QueryClientProvider, { client }, createElement(MemoryRouter, { initialEntries: [route] }, ui)));
}
