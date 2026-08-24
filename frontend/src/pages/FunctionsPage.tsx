import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Plus, Trash2, Loader2, FunctionSquare, Code2 } from 'lucide-react';
import { useFunctions, useCreateFunction, useDropFunction } from '../hooks/useSchema';
import { SidePanel } from '../components/ui/SidePanel';
import { ConfirmModal } from '../components/ui/ConfirmModal';

function getVolatilityClass(volatility: string): string {
  if (volatility === 'IMMUTABLE') return 'bg-green-500/10 text-green-400';
  if (volatility === 'STABLE') return 'bg-yellow-500/10 text-yellow-400';
  return 'bg-text-tertiary/10 text-text-tertiary';
}

export function FunctionsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: functions = [], isLoading } = useFunctions(projectId || '');
  const createFn = useCreateFunction(projectId || '');
  const dropFn = useDropFunction(projectId || '');

  const [showCreate, setShowCreate] = useState(false);
  const [dropTarget, setDropTarget] = useState<{ name: string; argTypes: string } | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  // Form
  const [fName, setFName] = useState('');
  const [fLang, setFLang] = useState('plpgsql');
  const [fReturn, setFReturn] = useState('void');
  const [fArgs, setFArgs] = useState('');
  const [fBody, setFBody] = useState('');
  const [fVol, setFVol] = useState('VOLATILE');

  const handleCreate = () => {
    if (!fName.trim() || !fBody.trim()) return;
    createFn.mutate(
      { name: fName, language: fLang, returnType: fReturn, args: fArgs || undefined, body: fBody, volatility: fVol },
      { onSuccess: () => { setShowCreate(false); setFName(''); setFBody(''); setFArgs(''); } }
    );
  };

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  return (
    <div data-testid="functions-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary">Database Functions</h3>
        <button
          onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="create-function-btn"
        >
          <Plus className="w-4 h-4" /> Create Function
        </button>
      </div>

      <div className="space-y-3">
        {functions.map(fn => (
          <div key={`${fn.name}(${fn.argTypes})`} className="rounded-lg border border-border-primary bg-surface-card overflow-hidden" data-testid={`fn-${fn.name}`}>
            <div className="flex items-center justify-between px-4 py-3">
              <button
                onClick={() => setExpanded(expanded === fn.name ? null : fn.name)}
                className="flex items-center gap-2 text-left flex-1"
              >
                <FunctionSquare className="w-4 h-4 text-purple-400" />
                <span className="text-sm font-medium text-text-primary">{fn.name}</span>
                <span className="text-xs text-text-tertiary">({fn.argTypes || 'void'})</span>
                <span className="text-xs text-text-tertiary ml-2">→ {fn.returnType}</span>
                <span className={`ml-2 px-1.5 py-0.5 text-[10px] rounded font-medium ${
                  fn.language === 'plpgsql' ? 'bg-blue-500/10 text-blue-400' : 'bg-green-500/10 text-green-400'
                }`}>{fn.language}</span>
                <span className={`px-1.5 py-0.5 text-[10px] rounded font-medium ${getVolatilityClass(fn.volatility)}`}>{fn.volatility}</span>
              </button>
              <button
                onClick={() => setDropTarget({ name: fn.name, argTypes: fn.argTypes })}
                className="p-1 text-text-tertiary hover:text-red-400 transition-colors"
              >
                <Trash2 className="w-4 h-4" />
              </button>
            </div>
            {expanded === fn.name && (
              <div className="px-4 pb-4 border-t border-border-primary">
                <div className="flex items-center gap-2 py-2">
                  <Code2 className="w-3.5 h-3.5 text-text-tertiary" />
                  <span className="text-xs text-text-tertiary">Definition</span>
                </div>
                <pre className="p-3 rounded-lg bg-bg-primary text-xs text-text-secondary font-mono overflow-x-auto max-h-64">
                  {fn.definition}
                </pre>
              </div>
            )}
          </div>
        ))}
        {functions.length === 0 && (
          <div className="text-center py-12 text-text-tertiary text-sm">
            <FunctionSquare className="w-10 h-10 mx-auto mb-2" />
            No functions defined yet
          </div>
        )}
      </div>

      <SidePanel
        open={showCreate}
        onClose={() => setShowCreate(false)}
        title="Create Function"
        width="w-[480px]"
        footer={
          <button onClick={handleCreate} disabled={!fName.trim() || !fBody.trim() || createFn.isPending}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50 transition-colors"
            data-testid="create-function-submit"
          >
            {createFn.isPending ? 'Creating...' : 'Create Function'}
          </button>
        }
      >
        <div className="space-y-4">
          <div>
            <label htmlFor="fn-name-input" className="block text-sm font-medium text-text-secondary mb-1">Function Name</label>
            <input id="fn-name-input" type="text" value={fName} onChange={e => setFName(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
              data-testid="fn-name-input" autoFocus />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label htmlFor="fn-lang-select" className="block text-sm font-medium text-text-secondary mb-1">Language</label>
              <select id="fn-lang-select" value={fLang} onChange={e => setFLang(e.target.value)}
                className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
                <option value="plpgsql">PL/pgSQL</option>
                <option value="sql">SQL</option>
              </select>
            </div>
            <div>
              <label htmlFor="fn-return-input" className="block text-sm font-medium text-text-secondary mb-1">Returns</label>
              <input id="fn-return-input" type="text" value={fReturn} onChange={e => setFReturn(e.target.value)}
                className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500" />
            </div>
          </div>
          <div>
            <label htmlFor="fn-args-input" className="block text-sm font-medium text-text-secondary mb-1">Arguments</label>
            <input id="fn-args-input" type="text" value={fArgs} onChange={e => setFArgs(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm font-mono focus:outline-none focus:ring-2 focus:ring-purple-500"
              placeholder="e.g. p_name text, p_age integer" />
          </div>
          <div>
            <label htmlFor="fn-vol-select" className="block text-sm font-medium text-text-secondary mb-1">Volatility</label>
            <select id="fn-vol-select" value={fVol} onChange={e => setFVol(e.target.value)}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500">
              <option value="VOLATILE">VOLATILE</option>
              <option value="STABLE">STABLE</option>
              <option value="IMMUTABLE">IMMUTABLE</option>
            </select>
          </div>
          <div>
            <label htmlFor="fn-body-input" className="block text-sm font-medium text-text-secondary mb-1">Function Body</label>
            <textarea id="fn-body-input" value={fBody} onChange={e => setFBody(e.target.value)} rows={10}
              className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm font-mono focus:outline-none focus:ring-2 focus:ring-purple-500"
              data-testid="fn-body-input"
              placeholder="BEGIN&#10;  RETURN 'hello';&#10;END;" />
          </div>
        </div>
      </SidePanel>

      <ConfirmModal
        open={!!dropTarget}
        onClose={() => setDropTarget(null)}
        onConfirm={() => { if (dropTarget) dropFn.mutate(dropTarget, { onSuccess: () => setDropTarget(null) }); }}
        title="Drop Function"
        message={`Are you sure you want to drop "${dropTarget?.name}"?`}
        confirmLabel="Drop Function"
        destructive
        loading={dropFn.isPending}
      />
    </div>
  );
}
