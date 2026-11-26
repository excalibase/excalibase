import { useState } from 'react';
import { Link } from 'react-router-dom';
import type { UseInfiniteQueryResult, InfiniteData } from '@tanstack/react-query';
import type { CursorPage, DomainCount, ProjectBrief, SuspendedBrief } from '../../../api/adminConsole';
import { serverErrorMessage } from '../../../utils/serverError';
import { formatInstant } from '../../../utils/instant';
import { relativeTime } from '../../../utils/relativeTime';
import { InfiniteColumn } from './InfiniteColumn';
import { groupRuns, type Run } from './groupRuns';

type PagedQuery<T> = UseInfiniteQueryResult<InfiniteData<CursorPage<T>>, Error>;

const ROW = 'flex w-full items-baseline justify-between gap-2 text-left text-sm rounded px-1 py-0.5 hover:bg-surface-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent-primary';

// What an infinite column needs from the paged query that feeds it.
function columnProps<T>(query: PagedQuery<T>, items: T[]) {
  return {
    total: query.data?.pages[0]?.total,
    count: items.length,
    isLoading: query.isLoading,
    loadError: query.isError && !query.data ? serverErrorMessage(query.error, 'Could not be read') : undefined,
    hasMore: query.hasNextPage,
    isLoadingMore: query.isFetchingNextPage,
    loadMoreError: query.isFetchNextPageError,
    onLoadMore: () => { void query.fetchNextPage(); },
    onRetry: () => { void query.refetch(); },
  };
}

const flatten = <T,>(query: PagedQuery<T>) => query.data?.pages.flatMap((page) => page.items) ?? [];

// A reason shared by a run of rows is said once, as the run's label.
function RunLabel({ text, count, tone }: { readonly text: string; readonly count: number; readonly tone: string }) {
  return (
    <div className={`truncate text-xs font-medium ${tone}`} title={text}>{text} · {count}</div>
  );
}

function Runs<T>({ runs, tone, row }: { readonly runs: Run<T>[]; readonly tone: string; readonly row: (item: T, showReason: boolean) => React.ReactNode }) {
  return runs.map((run, index) => (
    <li key={`${run.key}-${index}`} className="space-y-0.5">
      {run.items.length > 1 && run.key && <RunLabel text={run.key} count={run.items.length} tone={tone} />}
      <ul role="list" className="space-y-0.5">
        {run.items.map((item) => row(item, run.items.length === 1))}
      </ul>
    </li>
  ));
}

function SuspendedRow({ item, showReason }: { readonly item: SuspendedBrief; readonly showReason: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <li>
      <button type="button" aria-expanded={open} className={ROW} onClick={() => setOpen((value) => !value)}>
        <span className="truncate font-medium" title={item.email}>{item.email}</span>
        <time className="shrink-0 text-xs text-text-tertiary" dateTime={item.suspendedAt} title={formatInstant(item.suspendedAt)}>{relativeTime(item.suspendedAt)}</time>
      </button>
      {showReason && !open && <p className="truncate px-1 text-xs text-text-tertiary" title={item.reason}>{item.reason}</p>}
      {open && <p className="whitespace-pre-wrap px-1 text-xs text-text-secondary">{item.reason || 'No reason recorded.'} <span className="text-text-tertiary">({formatInstant(item.suspendedAt)})</span></p>}
    </li>
  );
}

export function SuspendedColumn({ query }: { readonly query: PagedQuery<SuspendedBrief> }) {
  const items = flatten(query);
  const runs = groupRuns(items, (item) => item.reason);
  return (
    <InfiniteColumn title="Newly suspended" emptyText="None in the last 24 h." {...columnProps(query, items)}>
      <Runs runs={runs} tone="text-text-secondary" row={(item, showReason) => <SuspendedRow key={item.userId} item={item} showReason={showReason} />} />
    </InfiniteColumn>
  );
}

function FailedRow({ item, showReason }: { readonly item: ProjectBrief; readonly showReason: boolean }) {
  const name = item.projectName || item.projectId;
  return (
    <li>
      <Link to={`/admin?project=${item.projectId}`} className={ROW} title={`Open ${name}`}>
        <span className="truncate font-medium">{name}</span>
        <span className="shrink-0 truncate text-xs text-text-tertiary max-w-[45%]" title={item.ownerEmail}>{item.ownerEmail}</span>
      </Link>
      {showReason && item.failureReason && <p className="truncate px-1 text-xs text-red-400" title={item.failureReason}>{item.failureReason}</p>}
    </li>
  );
}

export function FailedColumn({ query }: { readonly query: PagedQuery<ProjectBrief> }) {
  const items = flatten(query);
  const runs = groupRuns(items, (item) => item.failureReason ?? '');
  return (
    <InfiniteColumn title="Failed projects" emptyText="None." {...columnProps(query, items)}>
      <Runs runs={runs} tone="text-red-400" row={(item, showReason) => <FailedRow key={item.projectId} item={item} showReason={showReason} />} />
    </InfiniteColumn>
  );
}

// Sign-up domains are the day's top few, not a list that grows, but they sit in
// the same capped column so the three line up.
export function SignupsColumn({ signups, domains }: { readonly signups: number; readonly domains: DomainCount[] }) {
  return (
    <InfiniteColumn
      title="Sign-ups" total={signups} count={domains.length} isLoading={false} hasMore={false} isLoadingMore={false}
      onLoadMore={() => {}} onRetry={() => {}} emptyText="None."
    >
      <li className="text-sm">{signups} sign-ups in the last 24 h</li>
      {domains.map((item) => (
        <li key={item.domain} className="flex justify-between gap-2 text-sm">
          <span className="truncate" title={item.domain}>{item.domain}</span>
          <span className="text-text-secondary">{item.count}</span>
        </li>
      ))}
    </InfiniteColumn>
  );
}
