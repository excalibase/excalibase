import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { render, screen, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { InfiniteColumn, type InfiniteColumnProps } from './InfiniteColumn';

type Callback = (entries: { isIntersecting: boolean }[]) => void;
let observers: { callback: Callback; target?: Element; root?: Element | null; disconnected: boolean }[] = [];

beforeEach(() => {
  observers = [];
  vi.stubGlobal('IntersectionObserver', class {
    record: (typeof observers)[number];
    constructor(callback: Callback, options?: { root?: Element | null }) {
      this.record = { callback, root: options?.root, disconnected: false };
      observers.push(this.record);
    }
    observe(target: Element) { this.record.target = target; }
    disconnect() { this.record.disconnected = true; }
    unobserve() {}
  });
});
afterEach(() => vi.unstubAllGlobals());

const seeIt = () => act(() => { observers.at(-1)?.callback([{ isIntersecting: true }]); });

function column(over: Partial<InfiniteColumnProps> = {}) {
  const props: InfiniteColumnProps = {
    title: 'Newly suspended', total: 23, count: 2, isLoading: false, hasMore: true, isLoadingMore: false,
    onLoadMore: vi.fn(), onRetry: vi.fn(), emptyText: 'None.',
    children: [<li key="a">a@x.test</li>, <li key="b">b@x.test</li>],
    ...over,
  };
  return { props, ...render(<InfiniteColumn {...props} />) };
}

describe('InfiniteColumn', () => {
  test('the header shows the total and the list is a labelled, scrolling list', () => {
    column();
    expect(screen.getByRole('heading', { name: 'Newly suspended · 23' })).toBeInTheDocument();
    const list = screen.getByRole('list', { name: 'Newly suspended' });
    expect(list).toHaveAttribute('aria-busy', 'false');
    expect(list.parentElement).toHaveClass('max-h-[22rem]', 'overflow-y-auto');
  });

  test('the sentinel coming into view of the column asks for the next page once', () => {
    const { props } = column();
    expect(observers.at(-1)?.root).toBe(screen.getByRole('list').parentElement);
    seeIt();
    expect(props.onLoadMore).toHaveBeenCalledTimes(1);
  });

  test('it does not ask again while a page is loading, nor when there is no more', () => {
    const loading = column({ isLoadingMore: true });
    expect(screen.getByRole('list')).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    seeIt();
    expect(loading.props.onLoadMore).not.toHaveBeenCalled();
  });

  test('the last page ends with That\'s all and no observer is left running', () => {
    const { props } = column({ hasMore: false, count: 23 });
    expect(screen.getByText("That's all (23)")).toBeInTheDocument();
    seeIt();
    expect(props.onLoadMore).not.toHaveBeenCalled();
  });

  test('a failed next page shows a retry row, and retrying asks again instead of looping', async () => {
    const { props } = column({ loadMoreError: true });
    seeIt();
    expect(props.onLoadMore).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: /Retry/ }));
    expect(props.onLoadMore).toHaveBeenCalledTimes(1);
  });

  test('empty says None. and a failed first read offers a retry', async () => {
    const empty = column({ count: 0, total: 0, hasMore: false, children: [] });
    expect(screen.getByText('None.')).toBeInTheDocument();
    expect(screen.queryByText(/That's all/)).not.toBeInTheDocument();
    empty.unmount();
    const failed = column({ count: 0, total: undefined, hasMore: false, children: [], loadError: 'boom' });
    expect(screen.getByRole('alert')).toHaveTextContent('boom');
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(failed.props.onRetry).toHaveBeenCalled();
  });

  test('the first read shows Loading… and no count yet', () => {
    column({ isLoading: true, count: 0, total: undefined, children: [] });
    expect(screen.getByRole('heading', { name: 'Newly suspended' })).toBeInTheDocument();
    expect(screen.getByRole('list')).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByText('Loading…')).toBeInTheDocument();
  });

  test('the status row is always there, so rows arriving shift nothing', () => {
    const { rerender, props } = column({ hasMore: false });
    const before = screen.getByTestId('column-status');
    rerender(<InfiniteColumn {...props} hasMore isLoadingMore />);
    expect(screen.getByTestId('column-status')).toBe(before);
    expect(before).toHaveClass('min-h-8');
  });
});
