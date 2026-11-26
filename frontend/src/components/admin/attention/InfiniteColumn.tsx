import { useEffect, useRef, type ReactNode } from 'react';

export interface InfiniteColumnProps {
  readonly title: string;
  // The whole list's size on the server; unknown until the first page arrives.
  readonly total?: number;
  // Rows loaded so far.
  readonly count: number;
  readonly isLoading: boolean;
  readonly loadError?: string;
  readonly hasMore: boolean;
  readonly isLoadingMore: boolean;
  readonly loadMoreError?: boolean;
  readonly onLoadMore: () => void;
  readonly onRetry: () => void;
  readonly emptyText: string;
  readonly children: ReactNode;
}

// One column of the needs-attention card: it scrolls inside a fixed height, so
// the page never grows with the data, and loads the next page when the bottom
// row scrolls into view. The status row at the bottom always takes its height,
// so rows arriving, loading and the end state shift nothing.
export function InfiniteColumn(props: InfiniteColumnProps) {
  const { title, total, count, isLoading, loadError, hasMore, isLoadingMore, loadMoreError, onLoadMore, onRetry, emptyText, children } = props;
  const scroller = useRef<HTMLDivElement>(null);
  const sentinel = useRef<HTMLDivElement>(null);
  const canLoad = hasMore && !isLoadingMore && !loadMoreError && !isLoading;
  const load = useRef(onLoadMore);
  useEffect(() => { load.current = onLoadMore; });

  useEffect(() => {
    const target = sentinel.current;
    if (!canLoad || !target) return undefined;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) load.current();
    }, { root: scroller.current, rootMargin: '0px 0px 80px 0px' });
    observer.observe(target);
    return () => observer.disconnect();
  }, [canLoad]);

  const empty = !isLoading && !loadError && count === 0;
  return (
    <section className="bg-bg-secondary border border-border-primary rounded-lg p-3 min-w-0">
      <h3 className="text-xs uppercase tracking-wide text-text-tertiary mb-2">{total === undefined ? title : `${title} · ${total}`}</h3>
      <div ref={scroller} className="max-h-[22rem] overflow-y-auto overscroll-contain">
        <ul role="list" aria-label={title} aria-busy={isLoading || isLoadingMore} className="space-y-1">
          {children}
        </ul>
        {empty && <p className="text-sm text-text-tertiary">{emptyText}</p>}
        {loadError && (
          <div role="alert" className="text-sm text-red-400 flex items-center gap-2">
            <span>{loadError}</span>
            <button type="button" className="underline" onClick={onRetry}>Retry</button>
          </div>
        )}
        <div ref={sentinel} data-testid="column-status" className="min-h-8 flex items-center text-xs text-text-tertiary">
          {(isLoading || isLoadingMore) && 'Loading…'}
          {loadMoreError && !isLoadingMore && (
            <span className="text-red-400">Could not load more. <button type="button" className="underline" onClick={onLoadMore}>Retry</button></span>
          )}
          {!hasMore && !loadMoreError && count > 0 && `That's all (${count})`}
        </div>
      </div>
    </section>
  );
}
