import { useState, useRef, useEffect, useCallback } from 'react';
import { useParams } from 'react-router-dom';
import { Play, Clock, Trash2, Loader2, Plus, X } from 'lucide-react';
import { EditorView, keymap } from '@codemirror/view';
import { EditorState } from '@codemirror/state';
import { sql, PostgreSQL } from '@codemirror/lang-sql';
import { oneDark } from '@codemirror/theme-one-dark';
import { basicSetup } from 'codemirror';
import { useExecuteQuery, useTables } from '../hooks/useSchema';
import type { QueryResult } from '../types/schema';
import { cn } from '../utils/cn';

const TABS_KEY = 'sql_editor_tabs';
const HISTORY_KEY = 'sql_editor_history';
const MAX_HISTORY = 20;

interface SqlTab {
  id: string;
  title: string;
  content: string;
  result: QueryResult | null;
}

function loadTabs(): SqlTab[] {
  try {
    const saved = JSON.parse(localStorage.getItem(TABS_KEY) || '[]');
    return saved.length > 0 ? saved : [{ id: '1', title: 'Query 1', content: 'SELECT * FROM users;', result: null }];
  } catch {
    return [{ id: '1', title: 'Query 1', content: 'SELECT * FROM users;', result: null }];
  }
}

function saveTabs(tabs: SqlTab[]): void {
  localStorage.setItem(TABS_KEY, JSON.stringify(tabs.map(t => ({ ...t, result: null }))));
}

function loadHistory(): string[] {
  try { return JSON.parse(localStorage.getItem(HISTORY_KEY) || '[]'); } catch { return []; }
}

function saveHistory(history: string[]): void {
  localStorage.setItem(HISTORY_KEY, JSON.stringify(history.slice(0, MAX_HISTORY)));
}

function pluralRows(count: number): string {
  return `${count} row${count === 1 ? '' : 's'}`;
}

function formatAffectedRows(count: number | undefined): string {
  if (count === undefined) return '0 rows';
  return pluralRows(count);
}

// safeCellString avoids "[object Object]" stringification (S6551) when
// the SQL editor renders an arbitrary cell value from a query result.
function safeCellString(cell: unknown): string {
  if (cell === null || cell === undefined) return '';
  if (typeof cell === 'string') return cell;
  if (typeof cell === 'number' || typeof cell === 'boolean' || typeof cell === 'bigint') {
    return String(cell);
  }
  try {
    return JSON.stringify(cell);
  } catch {
    return '';
  }
}

export function SqlEditorPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [tabs, setTabs] = useState<SqlTab[]>(loadTabs);
  const [activeTabId, setActiveTabId] = useState(tabs[0]?.id || '1');
  const [history, setHistory] = useState<string[]>(loadHistory);
  const [showHistory, setShowHistory] = useState(false);
  const editorRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const executeQuery = useExecuteQuery(projectId ?? '');
  const { data: tableList = [] } = useTables(projectId ?? '');

  // Build schema object for autocomplete
  const [schemaMap, setSchemaMap] = useState<Record<string, string[]>>({});
  useEffect(() => {
    if (tableList.length === 0) return;
    const newMap: Record<string, string[]> = {};
    tableList.forEach(t => { newMap[t.name] = []; }); // columns populated lazily
    setSchemaMap(newMap);
  }, [tableList]);

  const activeTab = tabs.find(t => t.id === activeTabId) ?? tabs[0];

  // applyQueryResult lifts the tab-mutation logic out of executeQuery's
  // onSuccess callback so the run-query function stays under Sonar's
  // S2004 nesting limit.
  const applyQueryResult = useCallback((data: QueryResult) => {
    setTabs(prev => {
      const updated = prev.map(t => t.id === activeTabId
        ? { ...t, result: data, content: viewRef.current?.state.doc.toString() ?? t.content }
        : t);
      saveTabs(updated);
      return updated;
    });
  }, [activeTabId]);

  const runQueryRef = useRef<() => void>(() => {});
  runQueryRef.current = () => {
    if (!viewRef.current) return;
    // Run selected text if any, otherwise full editor content
    const sel = viewRef.current.state.selection.main;
    const hasSelection = sel.from < sel.to;
    const queryText = hasSelection
      ? viewRef.current.state.sliceDoc(sel.from, sel.to).trim()
      : viewRef.current.state.doc.toString().trim();
    if (!queryText) return;

    setHistory(prev => {
      const next = [queryText, ...prev.filter(q => q !== queryText)].slice(0, MAX_HISTORY);
      saveHistory(next);
      return next;
    });

    executeQuery.mutate(queryText, { onSuccess: applyQueryResult });
  };

  const runQuery = useCallback(() => runQueryRef.current(), []);

  // syncTabContent persists doc-change events into the active tab. Hoisted
  // out of the update listener to avoid 5-deep callback nesting (S2004).
  const syncTabContent = useCallback((content: string) => {
    setTabs(prev => prev.map(t => t.id === activeTabId ? { ...t, content } : t));
  }, [activeTabId]);

  // Create/destroy editor when active tab changes
  useEffect(() => {
    if (!editorRef.current) return;

    const state = EditorState.create({
      doc: activeTab?.content ?? '',
      extensions: [
        basicSetup,
        sql({ dialect: PostgreSQL, schema: schemaMap }),
        oneDark,
        keymap.of([{
          key: 'Ctrl-Enter',
          mac: 'Cmd-Enter',
          run: () => { runQueryRef.current(); return true; },
        }]),
        EditorView.theme({
          '&': { height: '220px', fontSize: '14px' },
          '.cm-scroller': { overflow: 'auto' },
        }),
        EditorView.updateListener.of(update => {
          if (update.docChanged) {
            syncTabContent(update.state.doc.toString());
          }
        }),
      ],
    });

    const view = new EditorView({ state, parent: editorRef.current });
    viewRef.current = view;
    return () => view.destroy();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeTabId, schemaMap]);

  const addTab = () => {
    const id = String(Date.now());
    const newTab: SqlTab = { id, title: `Query ${tabs.length + 1}`, content: '', result: null };
    const updated = [...tabs, newTab];
    setTabs(updated);
    saveTabs(updated);
    setActiveTabId(id);
  };

  const closeTab = (id: string) => {
    if (tabs.length === 1) return;
    const updated = tabs.filter(t => t.id !== id);
    setTabs(updated);
    saveTabs(updated);
    if (activeTabId === id) setActiveTabId(updated[0].id);
  };

  const result = activeTab?.result ?? null;

  const loadFromHistory = (q: string) => {
    if (viewRef.current) {
      viewRef.current.dispatch({ changes: { from: 0, to: viewRef.current.state.doc.length, insert: q } });
    }
    setShowHistory(false);
  };

  return (
    <div className="space-y-3" data-testid="sql-editor-page">
      {/* Tabs */}
      <div className="flex items-center gap-0 border-b border-border-primary" data-testid="sql-tabs">
        {tabs.map(tab => (
          <button
            key={tab.id}
            type="button"
            className={cn(
              'flex items-center gap-1.5 px-3 py-2 text-xs font-medium cursor-pointer border-b-2 transition-colors',
              tab.id === activeTabId
                ? 'border-purple-500 text-purple-400'
                : 'border-transparent text-text-tertiary hover:text-text-primary'
            )}
            onClick={() => setActiveTabId(tab.id)}
          >
            {tab.title}
            {tabs.length > 1 && (
              <button
                type="button"
                onClick={(e) => { e.stopPropagation(); closeTab(tab.id); }}
                className="p-0.5 rounded hover:bg-surface-hover"
                aria-label={`Close ${tab.title}`}
              >
                <X className="w-3 h-3" />
              </button>
            )}
          </button>
        ))}
        <button onClick={addTab} className="p-2 text-text-tertiary hover:text-text-primary" data-testid="add-tab-btn">
          <Plus className="w-3.5 h-3.5" />
        </button>
      </div>

      {/* Editor */}
      <div className="rounded-lg border border-border-primary overflow-hidden">
        <div ref={editorRef} data-testid="sql-editor" />
      </div>

      {/* Toolbar */}
      <div className="flex items-center gap-3">
        <button
          onClick={runQuery}
          disabled={executeQuery.isPending}
          className="flex items-center gap-2 px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors disabled:opacity-50"
          data-testid="run-query-btn"
        >
          {executeQuery.isPending ? <Loader2 className="w-4 h-4 animate-spin" /> : <Play className="w-4 h-4" />}
          Run {'⌘'}Enter
        </button>
        <button
          onClick={() => setShowHistory(!showHistory)}
          className="flex items-center gap-2 px-3 py-2 text-sm text-text-secondary hover:text-text-primary hover:bg-surface-hover rounded-lg transition-colors"
          data-testid="history-btn"
        >
          <Clock className="w-4 h-4" />
          History ({history.length})
        </button>
        <span className="ml-auto text-[10px] text-text-tertiary">
          Select text to run partial query
        </span>
      </div>

      {/* History dropdown */}
      {showHistory && history.length > 0 && (
        <div className="rounded-lg border border-border-primary bg-surface-card max-h-48 overflow-y-auto" data-testid="query-history">
          {history.map((q) => (
            <button key={q} onClick={() => loadFromHistory(q)}
              className="w-full text-left px-4 py-2 text-sm text-text-secondary hover:bg-surface-hover border-b border-border-primary last:border-0 font-mono truncate">
              {q}
            </button>
          ))}
          <button onClick={() => { setHistory([]); saveHistory([]); }}
            className="w-full flex items-center gap-2 px-4 py-2 text-sm text-red-400 hover:bg-surface-hover">
            <Trash2 className="w-3.5 h-3.5" /> Clear history
          </button>
        </div>
      )}

      {result && <ResultPanel result={result} />}
    </div>
  );
}

interface ResultPanelProps {
  readonly result: QueryResult;
}

// ResultPanel replaces the deeply-nested ternary in the parent (S3358) with a
// straightforward early-return chain.
function ResultPanel({ result }: ResultPanelProps) {
  return (
    <div className="rounded-lg border border-border-primary overflow-hidden" data-testid="query-results">
      <ResultBody result={result} />
    </div>
  );
}

function ResultBody({ result }: ResultPanelProps) {
  if (result.error) {
    return (
      <div className="p-4 bg-red-500/10 text-red-400 text-sm font-mono" data-testid="query-error">
        {result.error}
      </div>
    );
  }
  if (result.columns) {
    return <ResultTable result={result} />;
  }
  return (
    <div className="p-4 text-sm text-text-secondary" data-testid="query-success">
      {result.command}: {formatAffectedRows(result.affectedRows)} affected
    </div>
  );
}

function ResultTable({ result }: ResultPanelProps) {
  const rowCount = result.rows?.length ?? 0;
  const columns = result.columns ?? [];
  const rows = result.rows ?? [];
  return (
    <>
      <div className="px-4 py-2 bg-surface-card border-b border-border-primary text-xs text-text-tertiary">
        {pluralRows(rowCount)}
      </div>
      <div className="overflow-x-auto max-h-96">
        <table className="w-full text-sm">
          <thead className="sticky top-0">
            <tr className="bg-surface-card">
              {columns.map((col) => (
                <th key={col.name} className="px-4 py-2 text-left text-xs font-medium text-text-secondary border-b border-border-primary whitespace-nowrap">
                  {col.name} <span className="text-text-tertiary">{col.dataType}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, ri) => (
              <ResultRow key={makeRowKey(row, ri)} row={row} columns={columns} />
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

// makeRowKey builds a stable-ish key from row contents to satisfy S6479.
// SQL query results have no inherent identity; falling back on the row
// index would re-flag the rule. Hashing the serialised row gives a
// content-derived key that is stable across renders of the same result.
function makeRowKey(row: ReadonlyArray<unknown>, fallback: number): string {
  try {
    return JSON.stringify(row);
  } catch {
    return `row-${fallback}`;
  }
}

interface ResultRowProps {
  readonly row: ReadonlyArray<unknown>;
  readonly columns: ReadonlyArray<{ name: string }>;
}

function ResultRow({ row, columns }: ResultRowProps) {
  return (
    <tr className="hover:bg-surface-hover border-b border-border-primary last:border-0">
      {row.map((cell, ci) => {
        const key = `${columns[ci]?.name ?? 'col'}-${ci}`;
        return (
          <td key={key} className="px-4 py-2 text-text-primary font-mono text-xs whitespace-nowrap">
            {cell === null ? <span className="text-text-tertiary italic">NULL</span> : safeCellString(cell)}
          </td>
        );
      })}
    </tr>
  );
}
