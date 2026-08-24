import { useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { Loader2, Search, Radio } from 'lucide-react';
import {
  useRealtimeTables,
  useEnableRealtimeTable,
  useDisableRealtimeTable,
  useEnableAllRealtime,
  useDisableAllRealtime,
} from '../hooks/useRealtime';
import { ConfirmModal } from '../components/ui/ConfirmModal';

type ConfirmKind = 'enable-all' | 'disable-all' | null;

export function RealtimePage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { data: tables, isLoading } = useRealtimeTables(projectId);
  const enableTable = useEnableRealtimeTable(projectId);
  const disableTable = useDisableRealtimeTable(projectId);
  const enableAll = useEnableAllRealtime(projectId);
  const disableAll = useDisableAllRealtime(projectId);

  const [search, setSearch] = useState('');
  const [showOnlyEnabled, setShowOnlyEnabled] = useState(false);
  const [confirm, setConfirm] = useState<ConfirmKind>(null);

  const enabledCount = (tables ?? []).filter((t) => t.enabled).length;
  const totalCount = tables?.length ?? 0;

  const filtered = useMemo(() => {
    let rows = tables ?? [];
    if (showOnlyEnabled) rows = rows.filter((t) => t.enabled);
    if (search) {
      const q = search.toLowerCase();
      rows = rows.filter(
        (t) =>
          t.schema.toLowerCase().includes(q) || t.table.toLowerCase().includes(q),
      );
    }
    return rows;
  }, [tables, search, showOnlyEnabled]);

  const handleEnable = (schema: string, table: string) => enableTable.mutate({ schema, table });
  const handleDisable = (schema: string, table: string) => disableTable.mutate({ schema, table });

  return (
    <div data-testid="realtime-page">
      <div className="flex items-center justify-between mb-6">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-purple-500/10 border border-purple-500/30 flex items-center justify-center">
            <Radio className="w-5 h-5 text-purple-400" />
          </div>
          <div>
            <h3 className="text-lg font-semibold text-text-primary">Realtime</h3>
            <p className="text-sm text-text-secondary">
              Stream row-level changes for the tables you opt in. Subscribers receive
              INSERT/UPDATE/DELETE events via WebSocket.
            </p>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <button
            onClick={() => setConfirm('disable-all')}
            disabled={enabledCount === 0 || disableAll.isPending}
            className="px-3 py-2 text-sm rounded-lg border border-border-primary text-text-secondary hover:bg-surface-hover disabled:opacity-50 transition-colors"
            data-testid="realtime-disable-all"
          >
            Disable all
          </button>
          <button
            onClick={() => setConfirm('enable-all')}
            disabled={enabledCount === totalCount || totalCount === 0 || enableAll.isPending}
            className="px-3 py-2 text-sm rounded-lg bg-purple-500 hover:bg-purple-600 text-white disabled:opacity-50 transition-colors"
            data-testid="realtime-enable-all"
          >
            Enable all
          </button>
        </div>
      </div>

      <div className="bg-surface-card border border-border-primary rounded-lg p-4 mb-4 flex items-center justify-between">
        <span className="text-sm text-text-primary" data-testid="realtime-counter">
          <strong>{enabledCount}</strong>{' '}of{' '}<strong>{totalCount}</strong>{' '}tables enabled
        </span>
        <label className="flex items-center gap-2 text-xs text-text-secondary cursor-pointer">
          <input
            type="checkbox"
            checked={showOnlyEnabled}
            onChange={(e) => setShowOnlyEnabled(e.target.checked)}
            data-testid="realtime-filter-enabled"
          />
          {' '}Show only enabled
        </label>
      </div>

      <div className="relative mb-4">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-text-tertiary" />
        <input
          type="text"
          placeholder="Filter by schema or table..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full pl-10 pr-4 py-2 bg-bg-secondary border border-border-primary rounded-lg text-sm text-text-primary placeholder-text-tertiary focus:outline-none focus:ring-2 focus:ring-purple-500"
          data-testid="realtime-search"
        />
      </div>

      {isLoading && (
        <div className="flex justify-center py-12">
          <Loader2 className="w-6 h-6 animate-spin text-purple-400" />
        </div>
      )}

      {!isLoading && filtered.length === 0 && (
        <div className="text-center py-12 text-sm text-text-secondary">
          {search || showOnlyEnabled ? 'No tables match the filter.' : 'No user tables yet.'}
        </div>
      )}

      <div className="space-y-1" data-testid="realtime-list">
        {filtered.map((t) => {
          const id = `${t.schema}-${t.table}`;
          const isPending =
            (enableTable.isPending || disableTable.isPending) &&
            (enableTable.variables?.table === t.table ||
              disableTable.variables?.table === t.table);
          return (
            <div
              key={id}
              className="flex items-center justify-between px-4 py-3 bg-surface-card border border-border-primary rounded-lg"
              data-testid={`realtime-row-${id}`}
            >
              <div className="flex items-center gap-3 min-w-0">
                <code className="text-xs font-mono text-text-tertiary">{t.schema}</code>
                <span className="text-text-tertiary">.</span>
                <code className="text-sm font-mono text-text-primary truncate">{t.table}</code>
              </div>
              <label className="flex items-center gap-2 cursor-pointer">
                {isPending && <Loader2 className="w-3.5 h-3.5 animate-spin text-purple-400" />}
                <input
                  type="checkbox"
                  checked={t.enabled}
                  disabled={isPending}
                  onChange={() => t.enabled ? handleDisable(t.schema, t.table) : handleEnable(t.schema, t.table)}
                  data-testid={`realtime-toggle-${id}`}
                  className="cursor-pointer"
                />
                <span className="text-xs text-text-secondary">
                  {t.enabled ? 'Enabled' : 'Disabled'}
                </span>
              </label>
            </div>
          );
        })}
      </div>

      <ConfirmModal
        open={confirm === 'enable-all'}
        onClose={() => setConfirm(null)}
        onConfirm={() => {
          enableAll.mutate(undefined, { onSettled: () => setConfirm(null) });
        }}
        title="Enable realtime for all tables?"
        message={`This will start streaming WAL changes for all ${totalCount} user tables. Postgres CPU usage and WAL volume will increase. You can disable per-table afterward.`}
        confirmLabel="Enable all"
        loading={enableAll.isPending}
      />
      <ConfirmModal
        open={confirm === 'disable-all'}
        onClose={() => setConfirm(null)}
        onConfirm={() => {
          disableAll.mutate(undefined, { onSettled: () => setConfirm(null) });
        }}
        title="Disable realtime for all tables?"
        message={`Subscribers will stop receiving change events for all ${enabledCount} enabled tables. In-flight events already in NATS are not retracted.`}
        confirmLabel="Disable all"
        destructive
        loading={disableAll.isPending}
      />
    </div>
  );
}
