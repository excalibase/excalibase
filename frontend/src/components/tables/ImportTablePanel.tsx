import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { SidePanel } from '../ui/SidePanel';
import { ImportMapping } from './ImportMapping';
import { useTables } from '../../hooks/useSchema';
import {
  grantSelect,
  importErrorOf,
  importTable,
  previewImport,
  toImportOptions,
  IDENTIFIER_PATTERN,
  type ImportFailure,
  type ImportPreview,
  type ImportResult,
  type ImportSource,
  type ReadSettings,
  type TargetChoice,
} from '../../api/tableImport';

interface ImportTablePanelProps {
  readonly open: boolean;
  readonly onClose: () => void;
  readonly projectId: string;
}

type Step = 'source' | 'map' | 'result';
type Role = 'anon' | 'user';

const buttonClass =
  'w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50';

// A suggestion only: the server validates every name again.
function tableNameFrom(source: ImportSource): string {
  if (source.kind !== 'file') return 'imported_sheet';
  const name = source.file.name;
  const dot = name.lastIndexOf('.');
  const stem = dot > 0 ? name.slice(0, dot) : name;
  const base = trimUnderscores(stem.toLowerCase().replace(/[^a-z0-9_]+/g, '_'));
  if (!base) return 'imported';
  return (/^\d/.test(base) ? `t_${base}` : base).slice(0, 63);
}

function trimUnderscores(value: string): string {
  let start = 0;
  let end = value.length;
  while (start < end && value[start] === '_') start++;
  while (end > start && value[end - 1] === '_') end--;
  return value.slice(start, end);
}

function chosenSource(
  kind: ImportSource['kind'],
  file: File | null,
  sheetsUrl: string,
): ImportSource | null {
  if (kind === 'file') return file ? { kind: 'file', file } : null;
  const url = sheetsUrl.trim();
  return url ? { kind: 'sheets', url } : null;
}

function initialTarget(preview: ImportPreview, table: string): TargetChoice {
  return {
    schema: 'public',
    table,
    mode: 'create',
    primaryKey: '',
    columns: preview.columns.map((c) => ({ include: true, name: c.name, type: c.type })),
  };
}

function targetIsValid(target: TargetChoice): boolean {
  const included = target.columns.filter((c) => c.include);
  return (
    IDENTIFIER_PATTERN.test(target.schema) &&
    IDENTIFIER_PATTERN.test(target.table) &&
    included.length > 0 &&
    included.every((c) => IDENTIFIER_PATTERN.test(c.name))
  );
}

export function ImportTablePanel({ open, onClose, projectId }: ImportTablePanelProps) {
  const qc = useQueryClient();
  const { data: tables = [] } = useTables(projectId);
  const [step, setStep] = useState<Step>('source');
  const [kind, setKind] = useState<ImportSource['kind']>('file');
  const [file, setFile] = useState<File | null>(null);
  const [sheetsUrl, setSheetsUrl] = useState('');
  const [settings, setSettings] = useState<ReadSettings>({ hasHeader: true });
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [target, setTarget] = useState<TargetChoice | null>(null);
  const [grants, setGrants] = useState<Record<Role, boolean>>({ anon: false, user: false });
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState(0);
  const [failure, setFailure] = useState<ImportFailure | null>(null);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [granted, setGranted] = useState<Role[]>([]);

  const source = chosenSource(kind, file, sheetsUrl);

  const reset = () => {
    setStep('source');
    setFile(null);
    setSheetsUrl('');
    setSettings({ hasHeader: true });
    setPreview(null);
    setTarget(null);
    setGrants({ anon: false, user: false });
    setFailure(null);
    setResult(null);
    setGranted([]);
    setProgress(0);
  };
  const close = () => {
    reset();
    onClose();
  };

  const runPreview = async (next: ReadSettings) => {
    if (!source) return;
    setBusy(true);
    setFailure(null);
    try {
      const answer = await previewImport(projectId, source, next);
      setSettings(next);
      setPreview(answer);
      setTarget((prev) =>
        prev?.columns.length === answer.columns.length
          ? prev
          : initialTarget(answer, prev?.table ?? tableNameFrom(source)),
      );
      setStep('map');
    } catch (err) {
      setFailure(importErrorOf(err));
    } finally {
      setBusy(false);
    }
  };

  const runImport = async () => {
    if (!source || !preview || !target) return;
    setBusy(true);
    setFailure(null);
    setProgress(0);
    try {
      const done = await importTable(
        projectId,
        source,
        toImportOptions(preview, target),
        setProgress,
      );
      const roles =
        target.mode === 'create' ? (Object.keys(grants) as Role[]).filter((r) => grants[r]) : [];
      await Promise.all(roles.map((role) => grantSelect(projectId, done.schema, done.table, role)));
      setGranted(roles);
      setResult(done);
      setStep('result');
      qc.invalidateQueries({ queryKey: ['schema-tables', projectId] });
    } catch (err) {
      setFailure(importErrorOf(err));
    } finally {
      setBusy(false);
    }
  };

  const footer = (() => {
    if (step === 'source') {
      return (
        <button
          onClick={() => runPreview(settings)}
          disabled={!source || busy}
          className={buttonClass}
          data-testid="import-preview-btn"
        >
          {busy ? 'Reading...' : 'Preview'}
        </button>
      );
    }
    if (step === 'map') {
      return (
        <div className="flex gap-2">
          <button
            onClick={() => setStep('source')}
            disabled={busy}
            className="px-4 py-2 text-sm rounded-lg border border-border-primary text-text-secondary"
          >
            Back
          </button>
          <button
            onClick={runImport}
            disabled={busy || !target || !targetIsValid(target)}
            className={buttonClass}
            data-testid="import-submit"
          >
            {busy ? `Importing... ${progress}%` : 'Import'}
          </button>
        </div>
      );
    }
    return (
      <button onClick={close} className={buttonClass} data-testid="import-done">
        Done
      </button>
    );
  })();

  return (
    <SidePanel open={open} onClose={close} title="Import data" width="w-[40rem]" footer={footer}>
      {step === 'source' && (
        <SourceStep
          kind={kind}
          setKind={setKind}
          setFile={setFile}
          sheetsUrl={sheetsUrl}
          setSheetsUrl={setSheetsUrl}
          hasHeader={settings.hasHeader}
          setHasHeader={(h) => setSettings({ ...settings, hasHeader: h })}
        />
      )}
      {step === 'map' && preview && target && (
        <>
          <ImportMapping
            preview={preview}
            target={target}
            existingTables={tables.map((t) => t.name)}
            onTargetChange={setTarget}
            onReadChange={(change) => runPreview({ ...settings, ...change })}
          />
          {target.mode === 'create' && <GrantChoice grants={grants} setGrants={setGrants} />}
        </>
      )}
      {step === 'result' && result && <ResultStep result={result} granted={granted} />}
      {failure && <FailureView failure={failure} />}
    </SidePanel>
  );
}

interface SourceStepProps {
  readonly kind: ImportSource['kind'];
  readonly setKind: (k: ImportSource['kind']) => void;
  readonly setFile: (f: File | null) => void;
  readonly sheetsUrl: string;
  readonly setSheetsUrl: (u: string) => void;
  readonly hasHeader: boolean;
  readonly setHasHeader: (h: boolean) => void;
}

function SourceStep({
  kind,
  setKind,
  setFile,
  sheetsUrl,
  setSheetsUrl,
  hasHeader,
  setHasHeader,
}: SourceStepProps) {
  const tab = (value: ImportSource['kind'], label: string) => (
    <button
      onClick={() => setKind(value)}
      className={`px-3 py-1.5 text-xs rounded ${kind === value ? 'bg-purple-500/10 text-purple-400' : 'text-text-tertiary'}`}
      data-testid={`import-source-${value}`}
    >
      {label}
    </button>
  );
  return (
    <div className="space-y-4">
      <div className="flex gap-1">
        {tab('file', 'CSV / Excel file')}
        {tab('sheets', 'Google Sheets link')}
      </div>
      {kind === 'file' ? (
        <label className="block text-sm text-text-secondary">
          <span>CSV, TSV or XLSX file</span>
          <input
            type="file"
            accept=".csv,.tsv,.txt,.xlsx"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            className="block mt-1 text-xs"
            data-testid="import-file-input"
          />
        </label>
      ) : (
        <label className="block text-sm text-text-secondary">
          <span>
            Sheet link (shared as &quot;anyone with the link&quot; or published to the web)
          </span>
          <input
            value={sheetsUrl}
            onChange={(e) => setSheetsUrl(e.target.value)}
            placeholder="https://docs.google.com/spreadsheets/d/..."
            className="w-full mt-1 px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm"
            data-testid="import-sheets-url"
          />
        </label>
      )}
      <label className="flex items-center gap-2 text-sm text-text-secondary">
        <input
          type="checkbox"
          checked={hasHeader}
          onChange={(e) => setHasHeader(e.target.checked)}
        />
        <span>First row is a header</span>
      </label>
    </div>
  );
}

function GrantChoice({
  grants,
  setGrants,
}: {
  readonly grants: Record<Role, boolean>;
  readonly setGrants: (g: Record<Role, boolean>) => void;
}) {
  const box = (role: Role, label: string) => (
    <label className="flex items-center gap-2">
      <input
        type="checkbox"
        checked={grants[role]}
        onChange={(e) => setGrants({ ...grants, [role]: e.target.checked })}
        data-testid={`import-grant-${role}`}
      />
      {label}
    </label>
  );
  return (
    <div className="mt-4 space-y-1 text-xs text-text-secondary">
      <span className="block font-medium">API access</span>
      <p className="text-text-tertiary">
        A new table is private: no API role can read it until you add a permission.
      </p>
      {box('anon', 'Anyone (anon) can read every row')}
      {box('user', 'Signed-in users can read every row')}
    </div>
  );
}

function ResultStep({
  result,
  granted,
}: {
  readonly result: ImportResult;
  readonly granted: Role[];
}) {
  return (
    <div className="space-y-2 text-sm text-text-primary" data-testid="import-result">
      <p>
        Imported {result.rows} rows into {result.schema}.{result.table}.
      </p>
      <p className="text-xs text-text-tertiary">
        {granted.length === 0
          ? 'The table is private: no API role can read it until you add a permission.'
          : `Readable through the API by: ${granted.join(', ')}.`}
      </p>
    </div>
  );
}

function FailureView({ failure }: { readonly failure: ImportFailure }) {
  return (
    <div
      className="mt-4 p-3 rounded-lg border border-red-500/30 bg-red-500/5 text-xs text-red-400"
      data-testid="import-error"
    >
      <p>{failure.message}</p>
      {failure.rowErrors.length > 0 && (
        <table className="mt-2 w-full" data-testid="import-row-errors">
          <thead>
            <tr>
              <th className="text-left">Line</th>
              <th className="text-left">Column</th>
              <th className="text-left">Problem</th>
            </tr>
          </thead>
          <tbody>
            {failure.rowErrors.map((e) => (
              <tr key={`${e.line}-${e.column ?? ''}`}>
                <td>{e.line}</td>
                <td>{e.column ?? ''}</td>
                <td>
                  {e.value ? `"${e.value}" ` : ''}
                  {e.message}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
