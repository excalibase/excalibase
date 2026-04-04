import { useState } from 'react';
import { Plus, Trash2, Play, Loader2, Code2, Cpu, Circle } from 'lucide-react';
import { useEdgeFunctions, useCreateEdgeFunction, useDeleteEdgeFunction, useInvokeEdgeFunction, useRuntimeStatus } from '../hooks/useEdgeFunctions';
import { SidePanel } from '../components/ui/SidePanel';
import { ConfirmModal } from '../components/ui/ConfirmModal';
import type { EdgeFunction } from '../types/edgefn';

export function EdgeFunctionsPage() {
  const { data: functions = [], isLoading } = useEdgeFunctions();
  const { data: runtime } = useRuntimeStatus();
  const createFn = useCreateEdgeFunction();
  const deleteFn = useDeleteEdgeFunction();
  const invokeFn = useInvokeEdgeFunction();

  const [selected, setSelected] = useState<EdgeFunction | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [invokeResult, setInvokeResult] = useState<string | null>(null);

  // Create form
  const [fnId, setFnId] = useState('');
  const [fnName, setFnName] = useState('');
  const [fnCode, setFnCode] = useState('function handler(data) {\n  return { message: "Hello from edge!" };\n}');
  const [fnHookType, setFnHookType] = useState('custom');

  const handleCreate = () => {
    if (!fnId.trim() || !fnName.trim()) return;
    createFn.mutate({ id: fnId, name: fnName, code: fnCode, hookType: fnHookType }, {
      onSuccess: () => { setShowCreate(false); setFnId(''); setFnName(''); },
    });
  };

  const handleInvoke = (fn: EdgeFunction) => {
    invokeFn.mutate({ fnId: fn.id }, {
      onSuccess: (data) => setInvokeResult(JSON.stringify(data, null, 2)),
      onError: (err) => setInvokeResult(`Error: ${err.message}`),
    });
  };

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

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
        <button
          onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="create-fn-btn"
        >
          <Plus className="w-4 h-4" /> Deploy Function
        </button>
      </div>

      <div className="flex gap-4 h-[calc(100vh-220px)]">
        {/* Left: function list */}
        <div className="w-72 flex-shrink-0 border border-border-primary rounded-lg bg-surface-card overflow-hidden flex flex-col">
          <div className="px-4 py-3 border-b border-border-primary text-sm font-medium text-text-primary">
            Functions ({functions.length})
          </div>
          <div className="flex-1 overflow-y-auto">
            {functions.map(fn => (
              <button
                key={fn.id}
                onClick={() => { setSelected(fn); setInvokeResult(null); }}
                className={`w-full flex items-center gap-2 px-4 py-3 text-left border-b border-border-primary transition-colors ${
                  selected?.id === fn.id ? 'bg-purple-500/10 text-purple-400' : 'text-text-secondary hover:bg-surface-hover'
                }`}
                data-testid={`fn-item-${fn.id}`}
              >
                <Code2 className="w-4 h-4 flex-shrink-0" />
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-medium truncate">{fn.name}</div>
                  <div className="text-xs text-text-tertiary">{fn.hookType}</div>
                </div>
                {fn.active && <Circle className="w-2 h-2 fill-green-400 text-green-400 flex-shrink-0" />}
              </button>
            ))}
            {functions.length === 0 && (
              <p className="px-4 py-8 text-sm text-text-tertiary text-center">No functions deployed</p>
            )}
          </div>
        </div>

        {/* Right: detail view */}
        <div className="flex-1 border border-border-primary rounded-lg bg-surface-card overflow-hidden flex flex-col">
          {selected ? (
            <>
              <div className="flex items-center justify-between px-4 py-3 border-b border-border-primary">
                <div>
                  <h4 className="text-sm font-medium text-text-primary">{selected.name}</h4>
                  <span className="text-xs text-text-tertiary">ID: {selected.id} / v{selected.version}</span>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => handleInvoke(selected)}
                    disabled={invokeFn.isPending}
                    className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-green-400 hover:bg-green-500/10 rounded-lg transition-colors"
                    data-testid="invoke-fn-btn"
                  >
                    {invokeFn.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Play className="w-3.5 h-3.5" />} Invoke
                  </button>
                  <button
                    onClick={() => setDeleteTarget(selected.id)}
                    className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-red-400 hover:bg-red-500/10 rounded-lg transition-colors"
                    data-testid="delete-fn-btn"
                  >
                    <Trash2 className="w-3.5 h-3.5" /> Delete
                  </button>
                </div>
              </div>
              <div className="flex-1 overflow-auto">
                <div className="px-4 py-3 border-b border-border-primary">
                  <div className="flex items-center gap-2 mb-2">
                    <Cpu className="w-3.5 h-3.5 text-text-tertiary" />
                    <span className="text-xs text-text-tertiary">Source Code</span>
                  </div>
                  <pre className="p-3 rounded-lg bg-bg-primary text-xs text-text-secondary font-mono overflow-x-auto max-h-64" data-testid="fn-code">
                    {selected.code}
                  </pre>
                </div>
                {invokeResult && (
                  <div className="px-4 py-3">
                    <span className="text-xs text-text-tertiary block mb-2">Invocation Result</span>
                    <pre className="p-3 rounded-lg bg-bg-primary text-xs font-mono overflow-x-auto max-h-48 text-green-400" data-testid="invoke-result">
                      {invokeResult}
                    </pre>
                  </div>
                )}
                <div className="px-4 py-3 grid grid-cols-2 gap-4 text-xs">
                  <div><span className="text-text-tertiary">Hook Type</span><p className="text-text-primary font-medium">{selected.hookType}</p></div>
                  <div><span className="text-text-tertiary">Active</span><p className="text-text-primary font-medium">{selected.active ? 'Yes' : 'No'}</p></div>
                  <div><span className="text-text-tertiary">Created</span><p className="text-text-primary">{new Date(selected.createdAt).toLocaleString()}</p></div>
                  <div><span className="text-text-tertiary">Updated</span><p className="text-text-primary">{new Date(selected.updatedAt).toLocaleString()}</p></div>
                </div>
              </div>
            </>
          ) : (
            <div className="flex items-center justify-center h-full text-text-tertiary text-sm">
              Select a function or deploy a new one
            </div>
          )}
        </div>
      </div>

      {/* Create SidePanel */}
      <SidePanel open={showCreate} onClose={() => setShowCreate(false)} title="Deploy Edge Function" width="w-[480px]"
        footer={
          <button onClick={handleCreate} disabled={!fnId.trim() || !fnName.trim() || createFn.isPending}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50 transition-colors"
            data-testid="deploy-fn-submit"
          >{createFn.isPending ? 'Deploying...' : 'Deploy'}</button>
        }
      >
        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-text-secondary mb-1">Function ID</label>
            <input type="text" value={fnId} onChange={e => setFnId(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
              placeholder="e.g. hello-world" data-testid="fn-id-input" autoFocus />
          </div>
          <div>
            <label className="block text-sm font-medium text-text-secondary mb-1">Name</label>
            <input type="text" value={fnName} onChange={e => setFnName(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
              placeholder="e.g. Hello World" data-testid="fn-name-input" />
          </div>
          <div>
            <label className="block text-sm font-medium text-text-secondary mb-1">Hook Type</label>
            <select value={fnHookType} onChange={e => setFnHookType(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              <option value="custom">Custom</option>
              <option value="pre-provision">Pre-Provision</option>
              <option value="post-provision">Post-Provision</option>
              <option value="webhook">Webhook</option>
            </select>
          </div>
          <div>
            <label className="block text-sm font-medium text-text-secondary mb-1">Code</label>
            <textarea value={fnCode} onChange={e => setFnCode(e.target.value)} rows={12}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm font-mono focus:outline-none focus:ring-2 focus:ring-purple-500"
              data-testid="fn-code-input" />
          </div>
        </div>
      </SidePanel>

      <ConfirmModal open={!!deleteTarget} onClose={() => setDeleteTarget(null)}
        onConfirm={() => { if (deleteTarget) deleteFn.mutate(deleteTarget, { onSuccess: () => { setDeleteTarget(null); setSelected(null); } }); }}
        title="Delete Function" message={`Delete "${deleteTarget}"? This will stop the running function.`}
        confirmLabel="Delete" destructive loading={deleteFn.isPending} />
    </div>
  );
}
