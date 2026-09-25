import { useMemo, useState } from 'react';
import { json } from '@codemirror/lang-json';
import { CodeEditor } from './CodeEditor';
import { parseDocumentText } from '../../utils/mongoQuery';

interface DocumentEditorProps {
  readonly title: string;
  readonly initialText: string;
  readonly saving: boolean;
  readonly serverError: string | null;
  readonly onSave: (text: string) => void;
  readonly onCancel: () => void;
}

export function DocumentEditor({ title, initialText, saving, serverError, onSave, onCancel }: DocumentEditorProps) {
  const [text, setText] = useState(initialText);
  const validation = useMemo(() => parseDocumentText(text), [text]);
  const extensions = useMemo(() => [json()], []);

  const save = () => {
    if (validation.ok && !saving) onSave(text);
  };

  return (
    <div className="rounded-lg border border-purple-500/40 bg-surface-card p-3 space-y-2" data-testid="document-editor">
      <div className="text-sm font-medium text-text-primary">{title}</div>
      <CodeEditor
        value={text}
        onChange={setText}
        onSubmit={save}
        ariaLabel={title}
        testId="document-json"
        extensions={extensions}
        height="260px"
      />
      {!validation.ok && (
        <p role="alert" className="text-sm text-red-400">
          {validation.error}
        </p>
      )}
      {serverError && (
        <p role="alert" className="text-sm text-red-400">
          {serverError}
        </p>
      )}
      <div className="flex gap-2">
        <button
          type="button"
          onClick={save}
          disabled={!validation.ok || saving}
          className="px-3 py-1.5 rounded-md text-sm bg-purple-600 text-white hover:bg-purple-500 disabled:opacity-50"
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
        <button
          type="button"
          onClick={onCancel}
          className="px-3 py-1.5 rounded-md text-sm text-text-secondary hover:bg-surface-hover"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}
