import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { DataTable, type Column } from '../../ui/DataTable';
import { Button } from '../../Button';
import { useAuditLog } from '../../../hooks/useAdminConsole';
import type { AuditRow } from '../../../api/adminConsole';
import { formatInstant } from '../../../utils/instant';
import { relativeTime } from '../../../utils/relativeTime';
import { useDebounced } from '../useDebounced';

const FILTER_DELAY_MS = 300;
// Rows loaded by Load more all sit on one table page; the cursor does the paging.
const ONE_TABLE_PAGE = 10_000;
const FIELD = 'bg-bg-secondary border border-border-primary rounded-lg text-sm text-text-primary px-3 py-2 min-h-10 w-full';

export function auditTarget(row: AuditRow): { label: string; to?: string } {
  if (row.resource === 'user' && row.resourceId) return { label: row.targetEmail || `user ${row.resourceId}`, to: `/admin/users/${encodeURIComponent(row.resourceId)}` };
  if (!row.resource && !row.resourceId) return { label: '—' };
  return { label: [row.resource, row.resourceId].filter(Boolean).join(' ') };
}

const columns: Column<AuditRow>[] = [
  {
    id: 'when', header: 'When', locked: true,
    cell: (row) => (row.at
      ? <time dateTime={row.at} title={formatInstant(row.at)} className="whitespace-nowrap">{relativeTime(row.at)}</time>
      : '—'),
  },
  { id: 'who', header: 'Who', text: (row) => row.actorEmail ?? row.actorId ?? '', cell: (row) => row.actorEmail || row.actorId || <span className="text-text-tertiary">system</span> },
  { id: 'action', header: 'What', text: (row) => row.action, cell: (row) => <span className="font-mono text-xs">{row.action}</span> },
  {
    id: 'target', header: 'Target', text: (row) => auditTarget(row).label,
    cell: (row) => {
      const target = auditTarget(row);
      return target.to ? <Link to={target.to} className="underline">{target.label}</Link> : target.label;
    },
  },
  { id: 'details', header: 'Details', text: (row) => row.details ?? '', cell: (row) => <span className="block max-w-[28rem] truncate text-text-secondary" title={row.details}>{row.details}</span> },
];

function FilterField({ label, value, onChange, placeholder }: { readonly label: string; readonly value: string; readonly onChange: (value: string) => void; readonly placeholder: string }) {
  return (
    <label className="block min-w-0 text-xs text-text-tertiary">
      {label}
      <input type="text" className={`mt-1 ${FIELD}`} value={value} placeholder={placeholder} maxLength={200} onChange={(event) => onChange(event.target.value)} />
    </label>
  );
}

// The platform audit log: who did what to which target and when, newest first.
// The filters go to the server; the log is read by keyset, so Load more
// continues from where the last page ended however deep it goes.
export function AuditLogTab() {
  const [actor, setActor] = useState('');
  const [action, setAction] = useState('');
  const [target, setTarget] = useState('');
  const settled = useDebounced({ actor, action, target }, FILTER_DELAY_MS);
  const query = useAuditLog(settled);
  const rows = useMemo(() => query.data?.pages.flatMap((page) => page.items) ?? [], [query.data]);
  return (
    <div data-testid="audit-log" className="space-y-3">
      <p className="text-xs text-text-tertiary">Every audited action on the platform, newest first. A tenant&apos;s own tool calls are not listed.</p>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <FilterField label="Who" value={actor} onChange={setActor} placeholder="address or account id" />
        <FilterField label="Action" value={action} onChange={setAction} placeholder="starts with, e.g. user." />
        <FilterField label="Target" value={target} onChange={setTarget} placeholder="account, project or resource id" />
      </div>
      <DataTable
        label="Audit log"
        columns={columns}
        rows={rows}
        getRowId={(row) => row.id}
        isLoading={query.isLoading}
        error={query.isError && !query.data ? query.error : undefined}
        onRetry={() => { void query.refetch(); }}
        paging={{ mode: 'client', pageSize: ONE_TABLE_PAGE }}
        storageKey="admin-audit"
        emptyState="No audit entries match."
      />
      <div className="flex items-center gap-3 text-xs text-text-tertiary">
        {query.hasNextPage && (
          <Button size="sm" variant="secondary" disabled={query.isFetchingNextPage} onClick={() => { void query.fetchNextPage(); }}>
            {query.isFetchingNextPage ? 'Loading…' : 'Load more'}
          </Button>
        )}
        {query.isFetchNextPageError && <span role="alert" className="text-red-400">Could not load more. Try again.</span>}
        {!query.hasNextPage && rows.length > 0 && <span>{`That's all (${rows.length})`}</span>}
      </div>
    </div>
  );
}
