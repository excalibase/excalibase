import { useState } from 'react';
import { Loader2, Play, Trash2 } from 'lucide-react';
import { useApiUrl } from '../../../hooks/useDeploymentMode';
import { useDeleteEdgeFunction, useInvokeEdgeFunction } from '../../../hooks/useEdgeFunctions';
import { useProjectRole } from '../../../hooks/useProjectRole';
import type { EdgeFunction } from '../../../types/edgefn';
import { connectEndpoints } from '../../../utils/connectSnippets';
import { functionAnswer } from '../../../utils/edgeFunctions';
import { serverErrorMessage } from '../../../utils/serverError';
import { Endpoint } from '../../ConnectSnippets';
import { secondaryButton } from '../../containers/ContainerBits';
import { ConfirmModal } from '../../ui/ConfirmModal';
import { Notice } from './sections';

const INVOKE_BODY = JSON.stringify({ name: 'world' });

interface RowProps {
  readonly projectId: string;
  readonly fn: EdgeFunction;
  readonly invokeBase: string | undefined;
  readonly canDevelop: boolean;
  readonly onDelete: (id: string) => void;
}

function FunctionRow({ projectId, fn, invokeBase, canDevelop, onDelete }: RowProps) {
  const invoke = useInvokeEdgeFunction(projectId);
  const [result, setResult] = useState<string | null>(null);
  const run = () =>
    invoke.mutate(
      { fnId: fn.id, body: INVOKE_BODY },
      {
        onSuccess: (data) => setResult(typeof data === 'string' ? data : JSON.stringify(data, null, 2)),
        onError: (err) => setResult(functionAnswer(err) ?? `Error: ${serverErrorMessage(err, 'The function did not answer')}`),
      },
    );
  return (
    <li aria-label={fn.name} className="space-y-3 rounded-lg border border-border-primary bg-bg-secondary p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold text-text-primary">{fn.name}</p>
          <p className="truncate font-mono text-xs text-text-tertiary">{fn.id}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs text-text-secondary">
          <span>v{fn.version}</span>
          <span>{fn.files.length} {fn.files.length === 1 ? 'file' : 'files'}</span>
          <span className={fn.active ? 'text-green-400' : 'text-text-tertiary'}>{fn.active ? 'Active' : 'Inactive'}</span>
        </div>
      </div>
      {invokeBase ? (
        <Endpoint label="Invoke URL" value={`${invokeBase}/${fn.id}`} />
      ) : (
        <p className="text-xs text-text-secondary">This installation does not report its public API address, so no invoke URL can be shown.</p>
      )}
      {canDevelop && (
        <div className="flex flex-wrap gap-2">
          <button type="button" onClick={run} disabled={invoke.isPending} className={secondaryButton}>
            {invoke.isPending ? <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> : <Play className="h-4 w-4" aria-hidden="true" />} Invoke
          </button>
          <button type="button" onClick={() => onDelete(fn.id)} className={`${secondaryButton} text-red-400`}>
            <Trash2 className="h-4 w-4" aria-hidden="true" /> Delete
          </button>
        </div>
      )}
      {result !== null && <pre data-testid="invoke-result" className="max-h-48 overflow-auto rounded bg-bg-tertiary p-3 font-mono text-xs text-text-primary">{result}</pre>}
    </li>
  );
}

export function FunctionsTab({ projectId, functions }: { readonly projectId: string; readonly functions: readonly EdgeFunction[] }) {
  const { apiUrl, isLoading } = useApiUrl();
  const { canDevelop } = useProjectRole(projectId);
  const remove = useDeleteEdgeFunction(projectId);
  const [target, setTarget] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const invokeBase = apiUrl ? connectEndpoints(apiUrl, projectId).functions : undefined;
  const confirm = () => {
    if (!target) return;
    setError(null);
    remove.mutate(target, {
      onSuccess: () => setTarget(null),
      // Close the dialog so the reason is not hidden behind it.
      onError: (err) => {
        setTarget(null);
        setError(serverErrorMessage(err, 'The function was not deleted'));
      },
    });
  };
  if (isLoading) return null;
  return (
    <div className="space-y-3" data-testid="functions-list">
      {error && <p role="alert" className="text-sm text-red-400">{error}</p>}
      {functions.length === 0 && <Notice>No functions in this project.</Notice>}
      <ul className="space-y-3">
        {functions.map((fn) => (
          <FunctionRow key={fn.id} projectId={projectId} fn={fn} invokeBase={invokeBase} canDevelop={canDevelop} onDelete={setTarget} />
        ))}
      </ul>
      <ConfirmModal
        open={target !== null}
        onClose={() => setTarget(null)}
        onConfirm={confirm}
        title="Delete function"
        message={`This will remove "${target}" from this project.`}
        confirmText={target ?? ''}
        confirmLabel="Delete"
        destructive
        loading={remove.isPending}
      />
    </div>
  );
}
