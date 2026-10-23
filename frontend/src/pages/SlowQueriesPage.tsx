import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Loader2, Timer, RotateCcw } from 'lucide-react';
import { useEnableSlowQueries, useResetSlowQueries, useSlowQueries } from '../hooks/useSlowQueries';
import { ConfirmModal } from '../components/ui/ConfirmModal';
import { serverErrorMessage } from '../utils/serverError';
import { toast } from '../utils/toast';
import { enableNotice, formatDuration, shareOfTime, type SlowQueryReport, type SlowQuerySort } from '../utils/slowQueries';
import { cn } from '../utils/cn';

const SORTS: { key: SlowQuerySort; label: string }[] = [
  { key: 'total', label: 'Total time' },
  { key: 'mean', label: 'Mean time' },
];

export function SlowQueriesPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const [sort, setSort] = useState<SlowQuerySort>('total');
  const { data: report, isLoading, error } = useSlowQueries(projectId, sort);

  return (
    <div data-testid="slow-queries-page" className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold text-text-primary flex items-center gap-2">
          <Timer className="w-5 h-5 text-purple-400" /> Slow queries
        </h3>
        {report?.enabled && report.canReset && <ResetButton projectId={projectId} />}
      </div>
      {isLoading && (
        <div className="flex justify-center py-12">
          <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />
        </div>
      )}
      {error && !report && (
        <p role="alert" className="py-12 text-center text-sm text-red-400">
          {serverErrorMessage(error, 'Could not read the query statistics')}
        </p>
      )}
      {report && !report.enabled && <StatisticsOff projectId={projectId} report={report} />}
      {report?.enabled && (
        <>
          <div className="flex items-center gap-2">
            <span className="text-xs text-text-tertiary">Sort by</span>
            {SORTS.map(({ key, label }) => (
              <button
                key={key}
                type="button"
                onClick={() => setSort(key)}
                className={cn(
                  'px-3 py-1 rounded-md text-xs font-medium border',
                  sort === key
                    ? 'border-purple-400 text-purple-400 bg-purple-500/10'
                    : 'border-border-primary text-text-secondary hover:text-text-primary',
                )}
              >
                {label}
              </button>
            ))}
          </div>
          <QueryTable report={report} />
          <p className="text-xs text-text-tertiary">
            Statement text is normalized: values are replaced by placeholders. Only this project&apos;s own statements are listed.
          </p>
        </>
      )}
    </div>
  );
}

function QueryTable({ report }: { readonly report: SlowQueryReport }) {
  if (report.queries.length === 0) {
    return <p className="py-12 text-center text-sm text-text-secondary">No statements recorded yet.</p>;
  }
  return (
    <div className="overflow-x-auto bg-surface-card border border-border-primary rounded-xl">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-text-tertiary border-b border-border-primary text-left">
            <th className="py-2 px-4 font-medium">Statement</th>
            <th className="py-2 px-4 font-medium text-right">Calls</th>
            <th className="py-2 px-4 font-medium text-right">Total</th>
            <th className="py-2 px-4 font-medium text-right">Mean</th>
            <th className="py-2 px-4 font-medium text-right">Rows</th>
            <th className="py-2 px-4 font-medium text-right">Share</th>
          </tr>
        </thead>
        <tbody>
          {report.queries.map((q) => (
            <tr key={q.query} className="border-b border-border-primary last:border-0 align-top">
              <td className="py-3 px-4 font-mono text-xs text-text-primary max-w-xl whitespace-pre-wrap break-words">{q.query}</td>
              <td className="py-3 px-4 text-right text-text-secondary">{q.calls.toLocaleString('en-US')}</td>
              <td className="py-3 px-4 text-right text-text-secondary">{formatDuration(q.totalTimeMs)}</td>
              <td className="py-3 px-4 text-right text-yellow-400">{formatDuration(q.meanTimeMs)}</td>
              <td className="py-3 px-4 text-right text-text-secondary">{q.rows.toLocaleString('en-US')}</td>
              <td className="py-3 px-4 text-right text-text-tertiary">{shareOfTime(q, report.queries).toFixed(0)}%</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ResetButton({ projectId }: { readonly projectId: string }) {
  const [open, setOpen] = useState(false);
  const reset = useResetSlowQueries(projectId);
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium border border-border-primary text-text-secondary hover:text-text-primary"
      >
        <RotateCcw className="w-3.5 h-3.5" /> Reset statistics
      </button>
      <ConfirmModal
        open={open}
        onClose={() => setOpen(false)}
        title="Reset query statistics"
        message="Every statement's counts and times start again from zero."
        confirmLabel="Reset"
        loading={reset.isPending}
        onConfirm={() =>
          reset.mutateAsync().then(
            () => {
              setOpen(false);
              toast.success('Query statistics reset');
            },
            (err: unknown) => toast.error('Reset failed', { description: serverErrorMessage(err, 'The server did not say why.') }),
          )
        }
      />
    </>
  );
}

function StatisticsOff({ projectId, report }: { readonly projectId: string; readonly report: SlowQueryReport }) {
  const [open, setOpen] = useState(false);
  const enable = useEnableSlowQueries(projectId);
  return (
    <div className="max-w-xl mx-auto py-12 text-center space-y-4">
      <h4 className="text-base font-semibold text-text-primary">Query statistics are off for this database</h4>
      <p className="text-sm text-text-secondary">
        This database was created before query statistics were available.{' '}
        {report.canEnable ? enableNotice(report.engine) : 'An Admin of this project can turn them on.'}
      </p>
      {report.canEnable && (
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="px-4 py-2 rounded-md text-sm font-medium bg-purple-500 text-white hover:bg-purple-400"
        >
          Turn on query statistics
        </button>
      )}
      <ConfirmModal
        open={open}
        onClose={() => setOpen(false)}
        title="Turn on query statistics"
        message={enableNotice(report.engine)}
        confirmLabel="Turn on"
        loading={enable.isPending}
        onConfirm={() =>
          enable.mutateAsync().then(
            () => {
              setOpen(false);
              toast.success('Query statistics are turning on');
            },
            (err: unknown) => toast.error('Could not turn them on', { description: serverErrorMessage(err, 'The server did not say why.') }),
          )
        }
      />
    </div>
  );
}
