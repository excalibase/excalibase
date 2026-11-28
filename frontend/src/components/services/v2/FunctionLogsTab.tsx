import { useState } from 'react';
import { Loader2 } from 'lucide-react';
import { useEdgeFunctionLogs } from '../../../hooks/useEdgeFunctions';
import type { EdgeFunction } from '../../../types/edgefn';
import { formatInstant } from '../../../utils/instant';
import { serverErrorMessage } from '../../../utils/serverError';
import { Notice } from './sections';

const LEVEL_COLOR: Readonly<Record<string, string>> = { error: 'text-red-400', warn: 'text-yellow-400', info: 'text-blue-400' };

// The last 100 console lines of one function, polled while the tab is open.
export function FunctionLogsTab({ projectId, functions }: { readonly projectId: string; readonly functions: readonly EdgeFunction[] }) {
  const [chosen, setChosen] = useState('');
  const fnId = functions.some((fn) => fn.id === chosen) ? chosen : (functions[0]?.id ?? '');
  const { data: logs = [], isFetching, error } = useEdgeFunctionLogs(projectId, fnId || null);
  if (functions.length === 0) return <Notice>No functions in this project.</Notice>;
  return (
    <div className="space-y-3" data-testid="function-logs">
      <div className="flex flex-wrap items-center gap-3">
        <label htmlFor="function-logs-fn" className="text-xs text-text-tertiary">Function</label>
        <select
          id="function-logs-fn"
          value={fnId}
          onChange={(e) => setChosen(e.target.value)}
          className="rounded-lg border border-border-primary bg-bg-primary px-3 py-1.5 text-sm text-text-primary"
        >
          {functions.map((fn) => <option key={fn.id} value={fn.id}>{fn.id}</option>)}
        </select>
        {isFetching && <Loader2 className="h-3 w-3 animate-spin text-text-tertiary" aria-hidden="true" />}
      </div>
      <p className="text-xs text-text-tertiary">Last 100 lines from console.log, warn and error. Refreshes every 2 seconds.</p>
      <div className="max-h-[60vh] space-y-1 overflow-y-auto rounded-lg border border-border-primary bg-bg-tertiary p-3 font-mono text-xs">
        {error != null && <p role="alert" className="text-red-400">{serverErrorMessage(error, 'The logs could not be read')}</p>}
        {error == null && logs.length === 0 && <p className="text-text-tertiary">No logs yet. Invoke the function to generate output.</p>}
        {logs.map((entry, i) => (
          /* eslint-disable-next-line react/no-array-index-key -- log entries have no stable id; ts alone can collide under rapid invocation */
          <div key={`${entry.ts}-${i}`} className="flex gap-2">
            <span className="flex-shrink-0 text-text-tertiary">{formatInstant(entry.ts, 'time')}</span>
            <span className={`w-10 flex-shrink-0 pt-0.5 text-[10px] uppercase ${LEVEL_COLOR[entry.level] ?? 'text-text-secondary'}`}>{entry.level}</span>
            <span className="whitespace-pre-wrap break-words text-text-primary">{entry.msg}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
