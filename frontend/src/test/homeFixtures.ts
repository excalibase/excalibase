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
  tiers?: unknown[];
  features?: Record<string, boolean>;
  // The answer of GET /orgs/{id}/credits/; absent answers 500.
  credits?: unknown;
  // The answer of GET /orgs/{id}/plan-budget; absent answers 500.
  budget?: unknown;
  config?: Record<string, unknown>;
  failApps?: string[];
  failAllowed?: string[];
  // A request that never answers, to see a page while it loads.
  hangProjects?: boolean;
}

// Answers the Studio's GETs from `backend`; anything else is an empty list.
const reply = (data: unknown) => Promise.resolve({ data });
const fail = (status: number, message: string) => Promise.reject(Object.assign(new Error(message), { response: { status } }));

function configReply(backend: Backend) {
  return reply({ deploymentMode: 'cloud', appHosting: true, sites: true, ...backend.config, features: { studio_v2: true, dballowlist: true, mcp: true, ...backend.features } });
}

function provisionReply(backend: Backend) {
  if (backend.hangProjects) return new Promise(() => {});
  return backend.failProjects ? fail(403, 'forbidden') : reply(backend.projects);
}

// A GET under /projects/{id}/...: the project's apps, sites or allowed sources.
function scopedReply(backend: Backend, projectId: string, path: string) {
  if (path === 'apps') return backend.failApps?.includes(projectId) ? fail(500, 'boom') : reply(backend.apps?.[projectId] ?? []);
  if (path === 'sites') return reply(backend.sites?.[projectId] ?? []);
  if (path === 'db-endpoint/allowed-sources') {
    return backend.failAllowed?.includes(projectId) ? fail(500, 'boom') : reply(backend.allowed?.[projectId] ?? { access: 'listed', sources: [], publicEnabled: false });
  }
  return reply([]);
}

function answer(backend: Backend, url: string) {
  if (url === '/config') return configReply(backend);
  if (url === '/orgs') return reply(ORGS);
  if (url === '/provision') return provisionReply(backend);
  if (/^\/orgs\/[^/]+\/credits\/$/.test(url)) return backend.credits ? reply(backend.credits) : fail(500, 'boom');
  if (/^\/orgs\/[^/]+\/plan-budget$/.test(url)) return backend.budget ? reply(backend.budget) : fail(500, 'boom');
  if (url === '/tiers') return reply(backend.tiers ?? []);
  if (url === '/auth/tokens') return reply(backend.tokens ?? []);
  const scoped = /^\/projects\/([^/]+)\/(.*?)\/?$/.exec(url);
  return scoped ? scopedReply(backend, scoped[1], scoped[2]) : reply([]);
}

export function mountWith(backend: Backend, ui: ReactElement, route = '/') {
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'dev@example.test', role: 'user' }, isAuthenticated: true });
  useOrgSelection.setState({ orgId: null });
  vi.mocked(api.get).mockImplementation(((url: string) => answer(backend, url)) as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(createElement(QueryClientProvider, { client }, createElement(MemoryRouter, { initialEntries: [route] }, ui)));
}

export function creditOverview(extra: object = {}) {
  return {
    orgId: 'org-1', balanceMicros: 300_000_000, lots: [{ id: 1, kind: 'grant_trial', originalMicros: 300_000_000, remainingMicros: 300_000_000, expiresAt: '2027-02-25T00:00:00Z', createdAt: '2026-11-27T00:00:00Z' }],
    entries: [{ id: 1, kind: 'grant_trial', amountMicros: 300_000_000, actor: 'system', reason: 'sign-up credit', createdAt: '2026-11-27T00:00:00Z' }],
    burn: { perDayMicros: 0, days: [], runOutAt: null }, metering: true,
    nextExpiry: '2027-02-25T00:00:00Z', trial: { state: 'granted', amountMicros: 300_000_000, days: 90 }, billedIn: 'UTC', termsVersion: 'v1', termsUrl: 'https://example.test/terms', ...extra,
  };
}

const GIB = 1024 ** 3;

export function planBudget(extra: object = {}) {
  return {
    enabled: true, plan: 'FREE',
    budget: { cpuMilli: 2000, memoryBytes: 4 * GIB, diskBytes: 20 * GIB, services: 6 },
    used: { cpuMilli: 500, memoryBytes: GIB, diskBytes: 5 * GIB, services: 2 },
    left: { cpuMilli: 1500, memoryBytes: 3 * GIB, diskBytes: 15 * GIB, services: 4 },
    services: [
      { kind: 'database', name: 'shop', projectId: 'p1', size: { cpuMilli: 500, memoryBytes: GIB, diskBytes: 5 * GIB, services: 1 } },
      { kind: 'app', name: 'web', projectId: 'p1', size: { cpuMilli: 0, memoryBytes: 0, diskBytes: 0, services: 1 } },
    ],
    ...extra,
  };
}
