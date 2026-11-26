import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { api } from '../../../api/client';
import { useFailedProjects, useSuspensions } from '../../../hooks/useAdminConsole';
import { FailedColumn, SuspendedColumn } from './AttentionColumns';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn() } }));

type Callback = (entries: { isIntersecting: boolean }[]) => void;
let callbacks: Callback[] = [];
beforeEach(() => {
  vi.clearAllMocks();
  callbacks = [];
  vi.stubGlobal('IntersectionObserver', class {
    constructor(callback: Callback) { callbacks.push(callback); }
    observe() {}
    disconnect() {}
    unobserve() {}
  });
});
afterEach(() => vi.unstubAllGlobals());

const scrollToBottom = () => act(() => { callbacks.at(-1)?.([{ isIntersecting: true }]); });
const NOW = new Date().toISOString();
const person = (n: number, reason: string) => ({ userId: `u${n}`, email: `user${n}@x.test`, reason, suspendedAt: NOW });
const PROXY = 'Proxy apps refused';
const FIRST_PAGE = [person(1, PROXY), person(2, PROXY), person(3, PROXY), person(4, 'Mining found in the image')];
const SECOND_PAGE = [person(5, PROXY), person(6, 'Spam')];

function Suspended() {
  return <SuspendedColumn query={useSuspensions()} />;
}
function Failed() {
  return <FailedColumn query={useFailedProjects()} />;
}
function renderIn(ui: React.ReactElement) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>);
}
const cursorsAsked = () => vi.mocked(api.get).mock.calls.map(([, config]) => (config as { params: { cursor?: string } }).params.cursor);

describe('Newly suspended column', () => {
  test('says a shared reason once, with its count, and shows a unique reason as a muted line', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { items: FIRST_PAGE, total: 4 } } as never);
    renderIn(<Suspended />);
    expect(await screen.findByText(`${PROXY} · 3`)).toBeInTheDocument();
    expect(screen.queryAllByText(PROXY)).toHaveLength(0);
    expect(screen.getByText('Mining found in the image')).toHaveClass('truncate');
    expect(screen.getByRole('heading', { name: 'Newly suspended · 4' })).toBeInTheDocument();
    const row = screen.getByRole('button', { name: /user1@x.test/ });
    expect(within(row).getByText('just now')).toBeInTheDocument();
    expect(within(row).getByText('just now')).toHaveAttribute('title', expect.stringMatching(/\d/));
  });

  test('a row opens to its full reason and closes again, and can be reached with the keyboard', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { items: [person(1, 'A long reason that is cut off in the list')], total: 1 } } as never);
    renderIn(<Suspended />);
    const row = await screen.findByRole('button', { name: /user1@x.test/ });
    expect(row).toHaveAttribute('aria-expanded', 'false');
    row.focus();
    await userEvent.keyboard('{Enter}');
    expect(row).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText(/A long reason that is cut off in the list/)).not.toHaveClass('truncate');
    await userEvent.keyboard('{Enter}');
    expect(row).toHaveAttribute('aria-expanded', 'false');
  });

  test('scrolling to the bottom reads the next page with the cursor and appends it, ending with That\'s all', async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({ data: { items: FIRST_PAGE, total: 6, nextCursor: 'c1' } } as never)
      .mockResolvedValueOnce({ data: { items: SECOND_PAGE, total: 6 } } as never);
    renderIn(<Suspended />);
    await screen.findByText(`${PROXY} · 3`);
    expect(screen.queryByText(/That's all/)).not.toBeInTheDocument();
    scrollToBottom();
    expect(await screen.findByText("That's all (6)")).toBeInTheDocument();
    expect(cursorsAsked()).toEqual([undefined, 'c1']);
    expect(screen.getByText('user6@x.test')).toBeInTheDocument();
    expect(screen.getByRole('list', { name: 'Newly suspended' })).toHaveAttribute('aria-busy', 'false');
  });

  test('a failed next page keeps what is shown, offers a retry and carries on from the same cursor', async () => {
    vi.mocked(api.get)
      .mockResolvedValueOnce({ data: { items: FIRST_PAGE, total: 6, nextCursor: 'c1' } } as never)
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValueOnce({ data: { items: SECOND_PAGE, total: 6 } } as never);
    renderIn(<Suspended />);
    await screen.findByText(`${PROXY} · 3`);
    scrollToBottom();
    await userEvent.click(await screen.findByRole('button', { name: /Retry/ }));
    expect(await screen.findByText("That's all (6)")).toBeInTheDocument();
    expect(cursorsAsked()).toEqual([undefined, 'c1', 'c1']);
    expect(screen.getByText('user1@x.test')).toBeInTheDocument();
  });

  test('nothing suspended reads None in the last 24 h.', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { items: [], total: 0 } } as never);
    renderIn(<Suspended />);
    expect(await screen.findByText('None in the last 24 h.')).toBeInTheDocument();
    expect(screen.queryByText(/That's all/)).not.toBeInTheDocument();
  });
});

describe('Failed projects column', () => {
  test('rows link to the project, a shared failure is said once, and empty reads None.', async () => {
    const failed = (n: number, reason: string) => ({ projectId: `p${n}`, projectName: `proj${n}`, orgId: 'o', ownerEmail: `o${n}@x.test`, ownerStatus: 'active', status: 'FAILED', failureReason: reason });
    vi.mocked(api.get).mockResolvedValue({ data: { items: [failed(1, 'no capacity'), failed(2, 'no capacity'), failed(3, 'image pull')], total: 3 } } as never);
    const view = renderIn(<Failed />);
    expect(await screen.findByText('no capacity · 2')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /proj3/ })).toHaveAttribute('href', '/admin?project=p3');
    expect(screen.getByText('image pull')).toBeInTheDocument();
    view.unmount();
    vi.mocked(api.get).mockResolvedValue({ data: { items: [], total: 0 } } as never);
    renderIn(<Failed />);
    await waitFor(() => expect(screen.getByText('None.')).toBeInTheDocument());
  });
});
