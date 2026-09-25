import { useMemo, useRef, useState } from 'react';
import { autocompletion } from '@codemirror/autocomplete';
import { Play, RotateCcw } from 'lucide-react';
import { CodeEditor } from './CodeEditor';
import { mongoCompletionSource, toServerQuery, type QueryPart, type QueryText, type ServerQuery } from '../../utils/mongoQuery';

const emptyQuery: QueryText = { filter: '', sort: '', projection: '' };

const partLabels: Record<QueryPart, { label: string; placeholder: string; operators: boolean }> = {
  filter: { label: 'Filter', placeholder: "{ field: 'value' }", operators: true },
  projection: { label: 'Project', placeholder: '{ field: 1 }', operators: false },
  sort: { label: 'Sort', placeholder: '{ field: -1 }', operators: false },
};

interface QueryBarProps {
  readonly fields: readonly string[];
  readonly onApply: (query: ServerQuery) => void;
}

export function QueryBar({ fields, onApply }: QueryBarProps) {
  const [text, setText] = useState<QueryText>(emptyQuery);
  const [error, setError] = useState<string | null>(null);
  const fieldsRef = useRef(fields);
  fieldsRef.current = fields;

  const completions = useMemo(
    () =>
      Object.fromEntries(
        (Object.keys(partLabels) as QueryPart[]).map((part) => [
          part,
          [autocompletion({ override: [mongoCompletionSource(() => fieldsRef.current, partLabels[part].operators)] })],
        ]),
      ) as Record<QueryPart, ReturnType<typeof autocompletion>[]>,
    [],
  );

  const apply = () => {
    const result = toServerQuery(text);
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setError(null);
    onApply(result.value);
  };

  const reset = () => {
    setText(emptyQuery);
    setError(null);
    onApply({});
  };

  return (
    <div className="space-y-2" data-testid="query-bar">
      {(Object.keys(partLabels) as QueryPart[]).map((part) => (
        <div key={part} className="flex items-center gap-2">
          <span className="w-16 text-xs font-medium text-text-secondary">{partLabels[part].label}</span>
          <div className="flex-1 min-w-0">
            <CodeEditor
              value={text[part]}
              onChange={(value) => setText((prev) => ({ ...prev, [part]: value }))}
              onSubmit={apply}
              singleLine
              ariaLabel={`${partLabels[part].label} query`}
              testId={`query-${part}`}
              placeholder={partLabels[part].placeholder}
              extensions={completions[part]}
            />
          </div>
        </div>
      ))}
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={apply}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm bg-purple-600 text-white hover:bg-purple-500"
        >
          <Play className="w-3.5 h-3.5" /> Find
        </button>
        <button
          type="button"
          onClick={reset}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm text-text-secondary hover:bg-surface-hover"
        >
          <RotateCcw className="w-3.5 h-3.5" /> Reset
        </button>
        {error && (
          <span role="alert" className="text-sm text-red-400">
            {error}
          </span>
        )}
      </div>
    </div>
  );
}
