import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen } from '@testing-library/react';
import { Route, Routes } from 'react-router-dom';
import { DashboardPage } from './DashboardPage';
import { api } from '../api/client';
import { mountWith, project } from '../test/homeFixtures';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const routes = (
  <Routes>
    <Route path="/" element={<DashboardPage />} />
    <Route path="/admin" element={<div>operator console</div>} />
  </Routes>
);

const urlsAsked = () => vi.mocked(api.get).mock.calls.map(([url]) => String(url));

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
});

describe('DashboardPage', () => {
  test("flag off keeps the old dashboard and makes none of Home's requests", async () => {
    mountWith({ projects: [project('p1')], features: { studio_v2: false } }, routes);
    expect(await screen.findByText('Total Instances')).toBeInTheDocument();
    expect(screen.queryByTestId('home-page')).not.toBeInTheDocument();
    expect(urlsAsked().some((url) => url === '/auth/tokens' || url.includes('/apps') || url.includes('allowed-sources'))).toBe(false);
  });

  test('flag on shows Home', async () => {
    mountWith({ projects: [project('p1')] }, routes);
    expect(await screen.findByTestId('home-page')).toBeInTheDocument();
  });

  test('on the admin host the root still goes to the console with the flag on', async () => {
    mountWith({ projects: [], config: { adminHost: globalThis.location.hostname } }, routes);
    expect(await screen.findByText('operator console')).toBeInTheDocument();
    expect(screen.queryByTestId('home-page')).not.toBeInTheDocument();
  });
});
