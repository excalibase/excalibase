import { useState } from 'react';
import { useInstanceContext } from '../context/InstanceContext';
import { usePerformanceSummary, useTopQueries, useWaitEvents, useEnablePerformanceInsights } from '../hooks/usePerformance';
import { Zap, Clock, Database, AlertCircle, Settings, CheckCircle, type LucideIcon } from 'lucide-react';
import { formatBytes } from '../utils/formatBytes';
// formatBytes is used to render summary.databaseSizeBytes below.

type Tab = 'summary' | 'queries' | 'waits';

function pct(n: number | undefined) {
  return n == null ? '—' : `${n.toFixed(1)}%`;
}
function ms(n: number | undefined) {
  if (n == null) return '—';
  return n >= 1000 ? `${(n / 1000).toFixed(2)}s` : `${n.toFixed(1)}ms`;
}

function extractErrorMessage(err: unknown): string | undefined {
  if (err != null && typeof err === 'object' && 'response' in err) {
    const resp = (err as { response?: { data?: { error?: string } } }).response;
    return resp?.data?.error;
  }
  return undefined;
}

function isStatStatementsMissing(err: unknown): boolean {
  if (err != null && typeof err === 'object' && 'response' in err) {
    const resp = (err as { response?: { data?: { hint?: string; error?: string } } }).response;
    return resp?.data?.hint?.includes('pg_stat_statements') === true ||
      String(resp?.data?.error ?? '').includes('pg_stat_statements');
  }
  return false;
}

export function PerformancePage() {
  const { projectId } = useInstanceContext();
  const [tab, setTab] = useState<Tab>('summary');

  const { data: summary, isLoading: summaryLoading, isError: summaryError, error: summaryErr } = usePerformanceSummary(projectId);
  const { data: queries = [], isLoading: queriesLoading, isError: queriesError, error: queriesErr } = useTopQueries(projectId, 15);
  const { data: waits = [], isLoading: waitsLoading, isError: waitsError, error: waitsErr } = useWaitEvents(projectId);
  const enablePi = useEnablePerformanceInsights(projectId);

  const [toast, setToast] = useState<{ msg: string; ok: boolean } | null>(null);
  function showToast(msg: string, ok: boolean) {
    setToast({ msg, ok });
    setTimeout(() => setToast(null), 5000);
  }

  const pgStatMissing = isStatStatementsMissing(summaryErr) || isStatStatementsMissing(queriesErr) || isStatStatementsMissing(waitsErr);
  const anyLoading = summaryLoading || queriesLoading || waitsLoading;

  return (
    <div className="max-w-6xl mx-auto space-y-6">
      {toast && (
        <div className={`px-4 py-2 rounded-lg text-sm font-medium border ${toast.ok ? 'bg-green-900/20 text-green-400 border-green-500/30' : 'bg-red-900/20 text-red-400 border-red-500/30'}`}>
          {toast.msg}
        </div>
      )}

      {/* Full-page gate: extension not enabled */}
      {!anyLoading && pgStatMissing && projectId && (
        <div className="bg-surface-card border border-border-primary rounded-xl p-12 flex flex-col items-center text-center gap-6">
          <div className="w-16 h-16 rounded-full bg-yellow-900/20 border border-yellow-500/30 flex items-center justify-center">
            <Settings className="w-8 h-8 text-yellow-400" />
          </div>
          <div>
            <h2 className="text-lg font-semibold text-text-primary mb-2">Performance Insights not enabled</h2>
            <p className="text-sm text-text-secondary max-w-md">
              The <code className="px-1 py-0.5 bg-bg-tertiary rounded text-accent-primary text-xs">pg_stat_statements</code> extension
              is required to track query execution time, call counts, and cache efficiency.
              It is not currently active on <span className="text-text-primary font-medium">{projectId}</span>.
            </p>
          </div>

          <div className="grid grid-cols-3 gap-4 w-full max-w-lg text-left">
            {[
              { label: 'Top slow queries', desc: 'See which queries take the most time' },
              { label: 'Cache hit ratio', desc: 'Monitor buffer cache effectiveness' },
              { label: 'Wait events', desc: 'Identify lock contention and I/O waits' },
            ].map(({ label, desc }) => (
              <div key={label} className="bg-bg-secondary border border-border-primary rounded-lg p-3">
                <div className="flex items-center gap-1.5 mb-1">
                  <CheckCircle className="w-3.5 h-3.5 text-green-400 flex-shrink-0" />
                  <span className="text-xs font-medium text-text-primary">{label}</span>
                </div>
                <p className="text-xs text-text-tertiary">{desc}</p>
              </div>
            ))}
          </div>

          <div className="space-y-2">
            <button
              onClick={() => enablePi.mutate(undefined, {
                onSuccess: (r) => showToast(r.status ?? 'Enabled — cluster restart in progress (~30s)', true),
                onError: (e: unknown) => showToast(extractErrorMessage(e) ?? 'Failed to enable', false),
              })}
              disabled={enablePi.isPending}
              className="px-6 py-2.5 bg-yellow-500 hover:bg-yellow-400 disabled:opacity-50 text-black text-sm font-semibold rounded-lg transition-colors"
            >
              {enablePi.isPending ? 'Enabling…' : 'Enable pg_stat_statements'}
            </button>
            <p className="text-xs text-text-tertiary">Triggers a rolling cluster restart — takes ~30 seconds. No data loss.</p>
          </div>
        </div>
      )}
      {(anyLoading || !pgStatMissing || !projectId) && (
      <>

      {/* Tabs */}
      <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
        <div className="flex border-b border-border-primary px-4">
          {([
            { key: 'summary', label: 'Overview', icon: Database },
            { key: 'queries', label: 'Top Queries', icon: Zap },
            { key: 'waits',   label: 'Wait Events', icon: Clock },
          ] as { key: Tab; label: string; icon: LucideIcon }[]).map(({ key, label, icon: Icon }) => (
            <button
              key={key}
              onClick={() => setTab(key)}
              className={`flex items-center gap-2 px-4 py-3 text-sm font-medium border-b-2 -mb-px transition-colors ${tab === key ? 'border-accent-primary text-accent-primary' : 'border-transparent text-text-secondary hover:text-text-primary'}`}
            >
              <Icon className="w-4 h-4" />{label}
            </button>
          ))}
        </div>

        <div className="p-6">
          {/* Summary */}
          {tab === 'summary' && summaryLoading && (
            <div className="text-center py-12 text-text-secondary text-sm">Loading…</div>
          )}
          {tab === 'summary' && !summaryLoading && summaryError && (
            <div className="text-center py-12 text-text-secondary">
              <AlertCircle className="w-10 h-10 mx-auto mb-3 text-yellow-500/60" />
              <p className="text-sm text-text-primary font-medium mb-1">Performance data unavailable</p>
              <p className="text-xs text-text-tertiary max-w-sm mx-auto">
                {extractErrorMessage(summaryErr) ?? 'Could not fetch performance data.'}
              </p>
            </div>
          )}
          {tab === 'summary' && !summaryLoading && !summaryError && !summary && (
            <div className="text-center py-12 text-text-secondary text-sm">No data yet.</div>
          )}
          {tab === 'summary' && !summaryLoading && !summaryError && summary && (
            <div className="space-y-6">
              <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
                {[
                  { label: 'Cache Hit Ratio', value: pct(summary.cacheHitRatio) },
                  { label: 'Active Connections', value: `${summary.activeConnections ?? '—'} / ${summary.totalConnections ?? '—'}` },
                  { label: 'Database Size', value: summary.databaseSizeBytes == null ? '—' : formatBytes(summary.databaseSizeBytes) },
                  { label: 'Slow Queries', value: summary.slowQueryCount },
                ].map(({ label, value }) => (
                  <div key={label} className="bg-bg-secondary border border-border-primary rounded-xl p-4">
                    <p className="text-xs text-text-tertiary mb-1">{label}</p>
                    <p className="text-lg font-bold text-text-primary">{value}</p>
                  </div>
                ))}
              </div>
              <p className="text-xs text-text-tertiary">Last collected: {summary.collectedAt ? new Date(summary.collectedAt).toLocaleString() : '—'}</p>
            </div>
          )}

          {/* Top Queries */}
          {tab === 'queries' && queriesLoading && (
            <div className="text-center py-12 text-text-secondary text-sm">Loading…</div>
          )}
          {tab === 'queries' && !queriesLoading && queriesError && (
            <div className="text-center py-12 text-text-secondary">
              <AlertCircle className="w-10 h-10 mx-auto mb-3 text-yellow-500/60" />
              <p className="text-sm text-text-primary font-medium mb-1">Query stats unavailable</p>
              <p className="text-xs text-text-tertiary max-w-sm mx-auto">
                {extractErrorMessage(queriesErr) ?? 'Could not fetch query stats.'}
              </p>
            </div>
          )}
          {tab === 'queries' && !queriesLoading && !queriesError && queries.length === 0 && (
            <div className="text-center py-12 text-text-secondary text-sm">No query stats yet.</div>
          )}
          {tab === 'queries' && !queriesLoading && !queriesError && queries.length > 0 && (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-text-tertiary border-b border-border-primary text-left">
                    <th className="py-2 pr-4 font-medium">Query</th>
                    <th className="py-2 pr-4 font-medium text-right">Calls</th>
                    <th className="py-2 pr-4 font-medium text-right">Total Time</th>
                    <th className="py-2 pr-4 font-medium text-right">Avg Time</th>
                    <th className="py-2 pr-4 font-medium text-right">Rows</th>
                    <th className="py-2 font-medium text-right">Cache Hit</th>
                  </tr>
                </thead>
                <tbody>
                  {queries.map((q, i) => (
                    <tr key={`query-${i}-${q.query?.slice(0, 20)}`} className="border-b border-border-primary last:border-0 hover:bg-surface-hover">
                      <td className="py-3 pr-4 font-mono text-xs text-text-primary max-w-xs truncate" title={q.query}>{q.query}</td>
                      <td className="py-3 pr-4 text-right text-text-secondary">{q.calls?.toLocaleString()}</td>
                      <td className="py-3 pr-4 text-right text-text-secondary">{ms(q.totalTimeMs)}</td>
                      <td className="py-3 pr-4 text-right text-yellow-400">{ms(q.meanTimeMs)}</td>
                      <td className="py-3 pr-4 text-right text-text-secondary">{q.rows?.toLocaleString()}</td>
                      <td className="py-3 text-right text-green-400">{pct(q.hitPercent)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {/* Wait Events */}
          {tab === 'waits' && waitsLoading && (
            <div className="text-center py-12 text-text-secondary text-sm">Loading…</div>
          )}
          {tab === 'waits' && !waitsLoading && waitsError && (
            <div className="text-center py-12 text-text-secondary">
              <AlertCircle className="w-10 h-10 mx-auto mb-3 text-yellow-500/60" />
              <p className="text-sm text-text-primary font-medium mb-1">Wait events unavailable</p>
              <p className="text-xs text-text-tertiary max-w-sm mx-auto">
                {extractErrorMessage(waitsErr) ?? 'Could not fetch wait events.'}
              </p>
            </div>
          )}
          {tab === 'waits' && !waitsLoading && !waitsError && waits.length === 0 && (
            <div className="text-center py-12 text-text-secondary text-sm">No active wait events — database is idle.</div>
          )}
          {tab === 'waits' && !waitsLoading && !waitsError && waits.length > 0 && (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-text-tertiary border-b border-border-primary text-left">
                  <th className="py-2 pr-4 font-medium">PID</th>
                  <th className="py-2 pr-4 font-medium">Wait Type</th>
                  <th className="py-2 pr-4 font-medium">Event</th>
                  <th className="py-2 pr-4 font-medium">State</th>
                  <th className="py-2 pr-4 font-medium">Duration</th>
                  <th className="py-2 font-medium">Query</th>
                </tr>
              </thead>
              <tbody>
                {waits.map((w, i) => (
                  <tr key={w.pid || i} className="border-b border-border-primary last:border-0 hover:bg-surface-hover">
                    <td className="py-3 pr-4 font-mono text-xs text-text-primary">{w.pid}</td>
                    <td className="py-3 pr-4 text-text-secondary">{w.waitEventType}</td>
                    <td className="py-3 pr-4 text-accent-primary">{w.waitEvent ?? '—'}</td>
                    <td className="py-3 pr-4 text-text-tertiary">{w.state}</td>
                    <td className="py-3 pr-4 text-yellow-400">{w.duration}</td>
                    <td className="py-3 font-mono text-xs text-text-secondary max-w-xs truncate" title={w.query}>{w.query}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      </>
      )}
    </div>
  );
}
