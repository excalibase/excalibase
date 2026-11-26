import { Loader2, Search } from 'lucide-react';
import { keptFor, lineColor, useLogExplorer } from '../../../hooks/useLogExplorer';
import { sentence } from '../../../utils/sentence';
import { serverErrorMessage } from '../../../utils/serverError';
import type { DatabaseInstance } from '../../../types';
import { cn } from '../../../utils/cn';
import { LogDrainsCard } from '../../LogDrainsCard';
import { Notice, Section } from './sections';

type Explorer = ReturnType<typeof useLogExplorer>;

function Toolbar({ explorer }: { readonly explorer: Explorer }) {
  const { search, setSearch, ranges, range, setRangeLabel } = explorer;
  return (
    <div className="flex flex-wrap items-center gap-3">
      <div className="relative min-w-0 flex-1 basis-48">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-text-tertiary" aria-hidden="true" />
        <input
          type="search"
          aria-label="Search logs"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search logs..."
          className="w-full rounded-lg border border-border-primary bg-bg-primary py-2 pl-9 pr-3 text-sm text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500"
        />
      </div>
      <div className="inline-flex overflow-hidden rounded-lg border border-border-primary">
        {ranges.map((r) => (
          <button
            key={r.label}
            type="button"
            aria-pressed={r.label === range.label}
            onClick={() => setRangeLabel(r.label)}
            className={cn('px-3 py-2 text-xs font-medium', r.label === range.label ? 'bg-accent-primary/20 text-text-primary' : 'text-text-secondary hover:bg-surface-hover')}
          >
            {r.label}
          </button>
        ))}
      </div>
    </div>
  );
}

function Lines({ explorer }: { readonly explorer: Explorer }) {
  const { isLoading, error, logLines, search } = explorer;
  if (isLoading) return <div className="flex justify-center py-10"><Loader2 className="h-6 w-6 animate-spin text-purple-400" aria-hidden="true" /></div>;
  if (error) return <p role="alert" className="py-8 text-center text-sm text-red-400">{serverErrorMessage(error, 'The logs could not be loaded')}</p>;
  if (logLines.length === 0) return <p className="py-8 text-center text-sm text-text-tertiary">{search ? 'No matching log lines' : 'No logs available'}</p>;
  return (
    <div className="max-h-[28rem] space-y-0.5 overflow-y-auto p-3 font-mono text-xs">
      {logLines.map((line, i) => (
        <div key={`log-${i}-${line.slice(0, 30)}`} className={cn('whitespace-pre-wrap break-words py-0.5', lineColor(line))}>{line}</div>
      ))}
    </div>
  );
}

// Search and filter the database's log lines, then where they are sent on.
export function DbLogsTab({ project }: { readonly project: DatabaseInstance }) {
  const explorer = useLogExplorer(project.projectId);
  if (explorer.notOffered) {
    return (
      <div className="space-y-5" data-testid="db-logs">
        <Notice testId="logs-not-offered">{sentence(explorer.notOffered)}</Notice>
        <LogDrainsCard projectId={project.projectId} />
      </div>
    );
  }
  return (
    <div className="space-y-5" data-testid="db-logs">
      <Section id="db-logs-title" title="Logs">
        <Toolbar explorer={explorer} />
        {explorer.retentionHours ? (
          <p className="text-xs text-text-tertiary" data-testid="log-retention">Logs are kept for {keptFor(explorer.retentionHours)} on this plan.</p>
        ) : null}
        <div className="overflow-hidden rounded-lg border border-border-primary bg-bg-primary">
          <Lines explorer={explorer} />
        </div>
      </Section>
      <LogDrainsCard projectId={project.projectId} />
    </div>
  );
}
