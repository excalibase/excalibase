import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Plus, Trash2, Play, Loader2, Code2, Circle, Key, X, FileCode, Terminal } from 'lucide-react';
import {
  useEdgeFunctions,
  useCreateEdgeFunction,
  useDeleteEdgeFunction,
  useInvokeEdgeFunction,
  useRuntimeStatus,
  useEdgeSecrets,
  useSetEdgeSecret,
  useDeleteEdgeSecret,
  useEdgeFunctionLogs,
} from '../hooks/useEdgeFunctions';
import { SidePanel } from '../components/ui/SidePanel';
import { ConfirmModal } from '../components/ui/ConfirmModal';
import type { EdgeFunction, EdgeFile } from '../types/edgefn';

const DEFAULT_INDEX_TS = `export default async (req: Request): Promise<Response> => {
  const { name = 'world' } = await req.json().catch(() => ({}));
  return Response.json({ message: \`Hello \${name}\` });
};
`;

type EnvEntry = { key: string; value: string };

// parseEnv turns a .env-style blob into entries. Handles:
//   - blank lines, full-line comments (#)
//   - optional `export ` prefix
//   - single- and double-quoted values (inline literal, no escape expansion)
// Invalid lines are returned as errors instead of throwing.
function parseEnv(src: string): { entries: EnvEntry[]; errors: string[] } {
  const entries: EnvEntry[] = [];
  const errors: string[] = [];
  const lines = src.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line || line.startsWith('#')) continue;
    const bare = line.replace(/^export\s+/, '');
    const eqIdx = bare.indexOf('=');
    if (eqIdx <= 0) {
      errors.push(`line ${i + 1}: missing '='`);
      continue;
    }
    const key = bare.slice(0, eqIdx).trim();
    let value = bare.slice(eqIdx + 1).trim();
    if (
      (value.startsWith('"') && value.endsWith('"') && value.length >= 2) ||
      (value.startsWith("'") && value.endsWith("'") && value.length >= 2)
    ) {
      value = value.slice(1, -1);
    }
    if (!/^[A-Z][A-Z0-9_]*$/.test(key)) {
      errors.push(`line ${i + 1}: invalid key "${key}" (must be UPPER_SNAKE)`);
      continue;
    }
    entries.push({ key, value });
  }
  return { entries, errors };
}

export function EdgeFunctionsPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { data: functions = [], isLoading } = useEdgeFunctions(projectId);
  const { data: runtime } = useRuntimeStatus(projectId);
  const createFn = useCreateEdgeFunction(projectId);
  const deleteFn = useDeleteEdgeFunction(projectId);
  const invokeFn = useInvokeEdgeFunction(projectId);

  const { data: secrets = [] } = useEdgeSecrets(projectId);
  const setSecret = useSetEdgeSecret(projectId);
  const deleteSecret = useDeleteEdgeSecret(projectId);

  const [selected, setSelected] = useState<EdgeFunction | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [showSecrets, setShowSecrets] = useState(false);
  const [logsFor, setLogsFor] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [invokeResult, setInvokeResult] = useState<string | null>(null);

  const { data: logs = [], isFetching: logsFetching } = useEdgeFunctionLogs(projectId, logsFor);

  const [fnId, setFnId] = useState('');
  const [fnName, setFnName] = useState('');
  const [files, setFiles] = useState<EdgeFile[]>([{ path: 'index.ts', content: DEFAULT_INDEX_TS }]);
  const [activeFileIdx, setActiveFileIdx] = useState(0);

  const [secretKey, setSecretKey] = useState('');
  const [secretValue, setSecretValue] = useState('');
  const [envPaste, setEnvPaste] = useState('');
  const [envParsing, setEnvParsing] = useState(false);
  const [envStatus, setEnvStatus] = useState<string | null>(null);

  const updateFileContent = (content: string) => {
    setFiles((cur) => cur.map((f, i) => (i === activeFileIdx ? { ...f, content } : f)));
  };

  const addFile = () => {
    const newPath = `helper-${files.length}.ts`;
    setFiles((cur) => [...cur, { path: newPath, content: '// helper module\n' }]);
    setActiveFileIdx(files.length);
  };

  const removeFile = (idx: number) => {
    if (files[idx].path === 'index.ts') return;
    setFiles((cur) => cur.filter((_, i) => i !== idx));
    setActiveFileIdx(Math.max(0, idx - 1));
  };

  const renameFile = (idx: number, newPath: string) => {
    setFiles((cur) => cur.map((f, i) => (i === idx ? { ...f, path: newPath } : f)));
  };

  const resetCreateForm = () => {
    setFnId('');
    setFnName('');
    setFiles([{ path: 'index.ts', content: DEFAULT_INDEX_TS }]);
    setActiveFileIdx(0);
  };

  const handleCreate = () => {
    if (!fnId.trim() || !fnName.trim()) return;
    if (!files.some((f) => f.path === 'index.ts')) return;
    createFn.mutate(
      { id: fnId, name: fnName, files },
      {
        onSuccess: () => {
          setShowCreate(false);
          resetCreateForm();
        },
      },
    );
  };

  const handleInvoke = (fn: EdgeFunction) => {
    invokeFn.mutate(
      { fnId: fn.id, body: JSON.stringify({ name: 'world' }) },
      {
        onSuccess: (data) => setInvokeResult(typeof data === 'string' ? data : JSON.stringify(data, null, 2)),
        onError: (err: Error) => setInvokeResult(`Error: ${err.message}`),
      },
    );
  };

  const handleBulkEnvPaste = async () => {
    const { entries, errors } = parseEnv(envPaste);
    if (entries.length === 0) {
      setEnvStatus(errors.length > 0 ? errors.join('; ') : 'nothing to import');
      return;
    }
    setEnvParsing(true);
    setEnvStatus(null);
    let ok = 0;
    const failed: string[] = [];
    for (const entry of entries) {
      try {
        await setSecret.mutateAsync(entry);
        ok++;
      } catch (err) {
        failed.push(`${entry.key}: ${(err as Error).message}`);
      }
    }
    setEnvParsing(false);
    const parts: string[] = [`saved ${ok}/${entries.length}`];
    if (errors.length > 0) parts.push(`${errors.length} skipped`);
    if (failed.length > 0) parts.push(`${failed.length} failed`);
    setEnvStatus(parts.join(', '));
    if (failed.length === 0 && errors.length === 0) {
      setEnvPaste('');
    }
  };

  const handleSetSecret = () => {
    if (!secretKey.trim() || !secretValue.trim()) return;
    setSecret.mutate(
      { key: secretKey, value: secretValue },
      {
        onSuccess: () => {
          setSecretKey('');
          setSecretValue('');
        },
      },
    );
  };

  if (isLoading) {
    return (
      <div className="flex justify-center py-16">
        <Loader2 className="w-8 h-8 animate-spin text-purple-400" />
      </div>
    );
  }

  const logLevelColor = (level: string): string => {
    if (level === 'error') return 'text-red-400';
    if (level === 'warn') return 'text-yellow-400';
    if (level === 'info') return 'text-blue-400';
    return 'text-text-secondary';
  };

  return (
    <div data-testid="edge-functions-page">
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-3">
          <h3 className="text-lg font-semibold text-text-primary">Edge Functions</h3>
          <div className="flex items-center gap-1.5 text-xs">
            <Circle className={`w-2 h-2 fill-current ${runtime?.healthy ? 'text-green-400' : 'text-red-400'}`} />
            <span className="text-text-tertiary">Runtime {runtime?.status ?? 'unknown'}</span>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setShowSecrets(true)}
            className="flex items-center gap-2 px-3 py-2 bg-bg-tertiary hover:bg-surface-hover text-text-primary text-sm font-medium rounded-lg border border-border-primary transition-colors"
            data-testid="secrets-btn"
          >
            <Key className="w-4 h-4" /> Secrets ({secrets.length})
          </button>
          <button
            onClick={() => setShowCreate(true)}
            className="flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors"
            data-testid="create-fn-btn"
          >
            <Plus className="w-4 h-4" /> Deploy Function
          </button>
        </div>
      </div>

      <div className="flex gap-4 h-[calc(100vh-220px)]">
        <div className="w-72 flex-shrink-0 border border-border-primary rounded-lg bg-surface-card overflow-hidden flex flex-col">
          <div className="px-4 py-3 border-b border-border-primary text-sm font-medium text-text-primary">
            Functions ({functions.length})
          </div>
          <div className="flex-1 overflow-y-auto">
            {functions.length === 0 && (
              <div className="px-4 py-6 text-xs text-text-tertiary">
                No functions yet. Click Deploy Function to create one.
              </div>
            )}
            {functions.map((fn) => (
              <button
                key={fn.id}
                onClick={() => {
                  setSelected(fn);
                  setInvokeResult(null);
                }}
                className={`w-full flex items-center gap-2 px-4 py-3 text-left border-b border-border-primary transition-colors ${
                  selected?.id === fn.id ? 'bg-purple-500/10 text-purple-400' : 'text-text-secondary hover:bg-surface-hover'
                }`}
                data-testid={`fn-item-${fn.id}`}
              >
                <Code2 className="w-4 h-4 flex-shrink-0" />
                <div className="min-w-0 flex-1">
                  <div className="text-sm truncate">{fn.name}</div>
                  <div className="text-xs text-text-tertiary truncate">v{fn.version} · {fn.files.length} file(s)</div>
                </div>
              </button>
            ))}
          </div>
        </div>

        <div className="flex-1 border border-border-primary rounded-lg bg-surface-card overflow-hidden flex flex-col">
          {!selected && (
            <div className="flex-1 flex items-center justify-center text-text-tertiary text-sm">
              Select a function to view its source, test, and logs
            </div>
          )}
          {selected && (
            <>
              <div className="flex items-center justify-between px-4 py-3 border-b border-border-primary">
                <div className="flex items-center gap-3">
                  <span className="text-sm font-medium text-text-primary">{selected.name}</span>
                  <span className="text-xs text-text-tertiary font-mono">{selected.id}</span>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => handleInvoke(selected)}
                    disabled={invokeFn.isPending}
                    className="flex items-center gap-1.5 px-3 py-1.5 bg-green-500 hover:bg-green-600 text-white text-xs font-medium rounded-md disabled:opacity-50"
                    data-testid="invoke-btn"
                  >
                    {invokeFn.isPending ? <Loader2 className="w-3 h-3 animate-spin" /> : <Play className="w-3 h-3" />}
                    Invoke
                  </button>
                  <button
                    onClick={() => setLogsFor(selected.id)}
                    className="flex items-center gap-1.5 px-3 py-1.5 bg-bg-tertiary hover:bg-bg-quaternary text-text-primary text-xs font-medium rounded-md"
                    data-testid="logs-btn"
                  >
                    <Terminal className="w-3 h-3" />
                    Logs
                  </button>
                  <button
                    onClick={() => setDeleteTarget(selected.id)}
                    className="flex items-center gap-1.5 px-3 py-1.5 bg-red-500/10 hover:bg-red-500/20 text-red-400 text-xs font-medium rounded-md"
                    data-testid="delete-fn-btn"
                  >
                    <Trash2 className="w-3 h-3" />
                  </button>
                </div>
              </div>
              <div className="flex-1 flex overflow-hidden">
                <div className="w-56 border-r border-border-primary overflow-y-auto">
                  {selected.files.map((f) => (
                    <div
                      key={f.path}
                      className="flex items-center gap-2 px-3 py-2 text-xs text-text-secondary border-b border-border-primary"
                    >
                      <FileCode className="w-3.5 h-3.5 flex-shrink-0" />
                      <span className="truncate font-mono">{f.path}</span>
                    </div>
                  ))}
                </div>
                <pre className="flex-1 p-4 text-xs font-mono text-text-primary overflow-auto bg-bg-tertiary" data-testid="fn-code">
                  <code>{selected.files[0]?.content ?? ''}</code>
                </pre>
              </div>
              {invokeResult && (
                <div className="border-t border-border-primary p-4 bg-bg-tertiary max-h-48 overflow-auto">
                  <div className="text-xs text-text-tertiary mb-1">Invoke result</div>
                  <pre className="text-xs text-text-primary font-mono">{invokeResult}</pre>
                </div>
              )}
            </>
          )}
        </div>
      </div>

      <SidePanel open={showCreate} onClose={() => setShowCreate(false)} title="Deploy Function">
        <div className="space-y-4 p-4">
          <div>
            <label htmlFor="fn-id-field" className="block text-xs text-text-tertiary mb-1">ID (slug)</label>
            <input
              id="fn-id-field"
              type="text"
              value={fnId}
              onChange={(e) => setFnId(e.target.value)}
              placeholder="hello-world"
              className="w-full px-3 py-2 bg-bg-tertiary border border-border-primary rounded text-sm text-text-primary font-mono"
              data-testid="fn-id-input"
            />
          </div>
          <div>
            <label htmlFor="fn-name-field" className="block text-xs text-text-tertiary mb-1">Display name</label>
            <input
              id="fn-name-field"
              type="text"
              value={fnName}
              onChange={(e) => setFnName(e.target.value)}
              placeholder="Hello World"
              className="w-full px-3 py-2 bg-bg-tertiary border border-border-primary rounded text-sm text-text-primary"
            />
          </div>
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="block text-xs text-text-tertiary">Files</span>
              <button onClick={addFile} className="text-xs text-purple-400 hover:text-purple-300" data-testid="add-file-btn">
                + Add file
              </button>
            </div>
            <div className="flex gap-1 mb-2 flex-wrap">
              {files.map((f, i) => (
                <div
                  key={`${f.path}-${i}`}
                  className={`flex items-center gap-1 px-2 py-1 rounded text-xs font-mono border ${
                    activeFileIdx === i
                      ? 'bg-purple-500/20 border-purple-500/40 text-purple-300'
                      : 'bg-bg-tertiary border-border-primary text-text-secondary'
                  }`}
                >
                  <button onClick={() => setActiveFileIdx(i)}>{f.path}</button>
                  {f.path !== 'index.ts' && (
                    <button onClick={() => removeFile(i)} className="hover:text-red-400">
                      <X className="w-3 h-3" />
                    </button>
                  )}
                </div>
              ))}
            </div>
            {files[activeFileIdx] && files[activeFileIdx].path !== 'index.ts' && (
              <input
                type="text"
                value={files[activeFileIdx].path}
                onChange={(e) => renameFile(activeFileIdx, e.target.value)}
                className="w-full mb-2 px-2 py-1 bg-bg-tertiary border border-border-primary rounded text-xs text-text-primary font-mono"
              />
            )}
            <textarea
              value={files[activeFileIdx]?.content ?? ''}
              onChange={(e) => updateFileContent(e.target.value)}
              rows={14}
              className="w-full px-3 py-2 bg-bg-tertiary border border-border-primary rounded text-xs text-text-primary font-mono"
              spellCheck={false}
              data-testid="fn-code-input"
            />
          </div>
          <button
            onClick={handleCreate}
            disabled={createFn.isPending || !fnId.trim() || !fnName.trim()}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded disabled:opacity-50"
            data-testid="submit-fn-btn"
          >
            {createFn.isPending ? 'Deploying…' : 'Deploy'}
          </button>
          {createFn.error && (
            <div className="text-xs text-red-400 font-mono">{createFn.error instanceof Error ? createFn.error.message : String(createFn.error)}</div>
          )}
        </div>
      </SidePanel>

      <SidePanel open={showSecrets} onClose={() => setShowSecrets(false)} title="Function secrets">
        <div className="space-y-4 p-4">
          <p className="text-xs text-text-tertiary">
            Secrets are injected as env vars. In your function, read them via{' '}
            <code className="bg-bg-tertiary px-1 rounded">Deno.env.get('KEY')</code>.
          </p>
          <div>
            <label htmlFor="secret-key-input" className="block text-xs text-text-tertiary mb-1">Key</label>
            <input
              id="secret-key-input"
              type="text"
              value={secretKey}
              onChange={(e) => setSecretKey(e.target.value.toUpperCase())}
              placeholder="STRIPE_KEY"
              className="w-full px-3 py-2 bg-bg-tertiary border border-border-primary rounded text-sm text-text-primary font-mono"
            />
          </div>
          <div>
            <label htmlFor="secret-value-input" className="block text-xs text-text-tertiary mb-1">Value</label>
            <input
              id="secret-value-input"
              type="password"
              value={secretValue}
              onChange={(e) => setSecretValue(e.target.value)}
              placeholder="sk_test_…"
              className="w-full px-3 py-2 bg-bg-tertiary border border-border-primary rounded text-sm text-text-primary"
            />
          </div>
          <button
            onClick={handleSetSecret}
            disabled={setSecret.isPending || !secretKey.trim() || !secretValue.trim()}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded disabled:opacity-50"
            data-testid="save-secret-btn"
          >
            {setSecret.isPending ? 'Saving…' : 'Save secret'}
          </button>
          {setSecret.error && (
            <div className="text-xs text-red-400 font-mono">{setSecret.error instanceof Error ? setSecret.error.message : String(setSecret.error)}</div>
          )}

          <div className="border-t border-border-primary pt-4">
            <div className="text-xs text-text-tertiary mb-1">Paste .env (bulk import)</div>
            <textarea
              value={envPaste}
              onChange={(e) => setEnvPaste(e.target.value)}
              placeholder={'STRIPE_KEY=sk_test_...\nDATABASE_URL="postgres://..."\n# comments and blank lines are ignored'}
              rows={6}
              className="w-full px-3 py-2 bg-bg-tertiary border border-border-primary rounded text-xs text-text-primary font-mono"
              data-testid="env-paste-textarea"
            />
            <button
              onClick={handleBulkEnvPaste}
              disabled={envParsing || !envPaste.trim()}
              className="mt-2 w-full px-4 py-2 bg-bg-tertiary hover:bg-bg-quaternary text-text-primary text-sm font-medium rounded disabled:opacity-50"
              data-testid="env-paste-import-btn"
            >
              {envParsing && 'Importing…'}
              {!envParsing && !envPaste.trim() && 'Import from .env'}
              {!envParsing && envPaste.trim() && `Import from .env (${parseEnv(envPaste).entries.length})`}
            </button>
            {envStatus && (
              <div className="text-xs text-text-tertiary mt-2 font-mono" data-testid="env-paste-status">
                {envStatus}
              </div>
            )}
          </div>

          <div className="border-t border-border-primary pt-4">
            <div className="text-xs text-text-tertiary mb-2">Existing keys (values hidden)</div>
            {secrets.length === 0 && <div className="text-xs text-text-tertiary">No secrets set</div>}
            {secrets.map((s) => (
              <div key={s.key} className="flex items-center justify-between py-2 border-b border-border-primary">
                <code className="text-xs text-text-primary font-mono">{s.key}</code>
                <button
                  onClick={() => deleteSecret.mutate(s.key)}
                  className="text-xs text-red-400 hover:text-red-300"
                >
                  <Trash2 className="w-3 h-3" />
                </button>
              </div>
            ))}
          </div>
        </div>
      </SidePanel>

      <SidePanel
        open={!!logsFor}
        onClose={() => setLogsFor(null)}
        title={logsFor ? `Logs — ${logsFor}` : 'Logs'}
      >
        <div className="p-4 space-y-3">
          <div className="flex items-center justify-between">
            <p className="text-xs text-text-tertiary">
              Last 100 lines from <code className="bg-bg-tertiary px-1 rounded">console.log/warn/error</code>. Polls every 2s.
            </p>
            {logsFetching && <Loader2 className="w-3 h-3 animate-spin text-text-tertiary" />}
          </div>
          <div
            className="bg-bg-tertiary border border-border-primary rounded p-3 max-h-[70vh] overflow-y-auto font-mono text-xs space-y-1"
            data-testid="logs-panel"
          >
            {logs.length === 0 && (
              <div className="text-text-tertiary">
                No logs yet. Invoke the function to generate output.
              </div>
            )}
            {logs.map((entry, i) => {
              const when = new Date(entry.ts).toLocaleTimeString();
              return (
                /* eslint-disable-next-line react/no-array-index-key -- log entries have no stable id; ts alone can collide under rapid invocation */
                <div key={`${entry.ts}-${i}`} className="flex gap-2">
                  <span className="text-text-tertiary flex-shrink-0">{when}</span>
                  <span className={`${logLevelColor(entry.level)} uppercase text-[10px] w-10 flex-shrink-0 pt-0.5`}>
                    {entry.level}
                  </span>
                  <span className="text-text-primary whitespace-pre-wrap break-words">{entry.msg}</span>
                </div>
              );
            })}
          </div>
        </div>
      </SidePanel>

      <ConfirmModal
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (deleteTarget) {
            deleteFn.mutate(deleteTarget, {
              onSuccess: () => {
                setSelected(null);
                setDeleteTarget(null);
              },
            });
          }
        }}
        title="Delete function"
        message={`This will remove "${deleteTarget}" from this project.`}
        confirmText={deleteTarget ?? ''}
        confirmLabel="Delete"
        destructive
        loading={deleteFn.isPending}
      />
    </div>
  );
}
