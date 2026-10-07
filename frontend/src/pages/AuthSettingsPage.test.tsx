import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AuthSettingsPage } from './AuthSettingsPage';
import { siteUrlProblem, suggestSiteUrl } from '../api/authSettings';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));

const SETTINGS_PATH = '/projects/proj-1/auth-settings';

function serve(settings: { siteUrl: string; requireEmailVerification: boolean }, apps: Array<{ url?: string }> = []) {
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    if (path === SETTINGS_PATH) return { data: settings };
    return { data: apps };
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/auth/settings']}>
        <Routes>
          <Route path="/project/:projectId/auth/settings" element={<AuthSettingsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const saveButton = () => screen.getByRole('button', { name: 'Save' });
const siteInput = () => screen.getByLabelText('Site URL') as HTMLInputElement;

beforeEach(() => {
  vi.resetAllMocks();
});

describe('siteUrlProblem', () => {
  test.each([
    ['', null],
    ['https://app.example.com', null],
    ['https://app.example.com/base', null],
    ['http://localhost:3000', null],
    ['http://127.0.0.1:5173', null],
  ])('accepts %j', (value, expected) => {
    expect(siteUrlProblem(value)).toBe(expected);
  });

  test.each([
    ['app.example.com', 'absolute'],
    ['/relative', 'absolute'],
    ['http://app.example.com', 'localhost'],
    ['ftp://app.example.com', 'https://'],
    ['https://user:pw@app.example.com', 'user and password'],
    ['https://app.example.com?x=1', 'query'],
    ['https://app.example.com#top', 'query'],
    ['https://app.example.com/', 'trailing slash'],
  ])('rejects %j', (value, fragment) => {
    expect(siteUrlProblem(value)).toContain(fragment);
  });
});

describe('suggestSiteUrl', () => {
  test('takes the first usable app address and drops a trailing slash', () => {
    expect(suggestSiteUrl([{}, { url: 'not a url' }, { url: 'https://shop.example.com/' }, { url: 'https://other.example.com' }]))
      .toBe('https://shop.example.com');
  });
  test('is null with no apps or no address', () => {
    expect(suggestSiteUrl(undefined)).toBeNull();
    expect(suggestSiteUrl([{ url: '' }])).toBeNull();
  });
});

describe('AuthSettingsPage', () => {
  test('warns prominently when no site URL is set', async () => {
    serve({ siteUrl: '', requireEmailVerification: false });
    renderPage();
    expect(await screen.findByTestId('site-url-warning')).toHaveTextContent('No site URL is set');
  });

  test('shows no warning once a site URL is set', async () => {
    serve({ siteUrl: 'https://app.example.com', requireEmailVerification: false });
    renderPage();
    await screen.findByTestId('auth-settings-form');
    expect(screen.queryByTestId('site-url-warning')).toBeNull();
    expect(siteInput().value).toBe('https://app.example.com');
  });

  test('suggests the project app address and fills it in on request', async () => {
    serve({ siteUrl: '', requireEmailVerification: false }, [{ url: 'https://shop.example.com' }]);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Use this address' }));
    expect(siteInput().value).toBe('https://shop.example.com');
    expect(screen.queryByRole('button', { name: 'Use this address' })).toBeNull();
  });

  test('offers no suggestion when no app has an address', async () => {
    serve({ siteUrl: '', requireEmailVerification: false }, [{}]);
    renderPage();
    await screen.findByTestId('auth-settings-form');
    expect(screen.queryByRole('button', { name: 'Use this address' })).toBeNull();
  });

  test('blocks an invalid address and says why', async () => {
    serve({ siteUrl: '', requireEmailVerification: false });
    renderPage();
    await screen.findByTestId('auth-settings-form');
    fireEvent.change(siteInput(), { target: { value: 'http://app.example.com' } });
    expect(screen.getByTestId('site-url-help')).toHaveTextContent('Plain http is only allowed for localhost');
    expect(saveButton()).toBeDisabled();
  });

  test('requiring verification needs a site URL', async () => {
    serve({ siteUrl: '', requireEmailVerification: false });
    renderPage();
    await screen.findByTestId('auth-settings-form');
    fireEvent.click(screen.getByLabelText(/Require email verification/));
    expect(screen.getByTestId('verification-needs-url')).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
    fireEvent.change(siteInput(), { target: { value: 'http://localhost:3000' } });
    expect(saveButton()).toBeEnabled();
  });

  test('saves the trimmed address and confirms', async () => {
    serve({ siteUrl: '', requireEmailVerification: false });
    vi.mocked(api.put).mockResolvedValue({ data: { siteUrl: 'https://app.example.com', requireEmailVerification: true } });
    renderPage();
    await screen.findByTestId('auth-settings-form');
    fireEvent.change(siteInput(), { target: { value: ' https://app.example.com ' } });
    fireEvent.click(screen.getByLabelText(/Require email verification/));
    fireEvent.click(saveButton());
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(SETTINGS_PATH, {
      siteUrl: 'https://app.example.com', requireEmailVerification: true,
    }));
    expect(await screen.findByTestId('auth-settings-saved')).toBeInTheDocument();
  });

  test('shows the server refusal as is', async () => {
    serve({ siteUrl: '', requireEmailVerification: false });
    vi.mocked(api.put).mockRejectedValue({ response: { status: 400, data: { error: 'invalid site url: nope' } } });
    renderPage();
    await screen.findByTestId('auth-settings-form');
    fireEvent.change(siteInput(), { target: { value: 'https://app.example.com' } });
    fireEvent.click(saveButton());
    expect(await screen.findByText('invalid site url: nope')).toBeInTheDocument();
  });

  test('says so when the settings cannot be loaded', async () => {
    vi.mocked(api.get).mockRejectedValue(new Error('down'));
    renderPage();
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load the auth settings');
  });
});
