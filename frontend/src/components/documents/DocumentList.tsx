import { useMemo, useState } from 'react';
import { ChevronLeft, ChevronRight, Loader2, Pencil, Plus } from 'lucide-react';
import { QueryBar } from './QueryBar';
import { DocumentEditor } from './DocumentEditor';
import { InlineConfirm } from './InlineConfirm';
import { apiErrorMessage, type CollectionRef, type MongoDocument } from '../../api/documents';
import {
  useDeleteDocument,
  useDocumentCount,
  useDocumentPage,
  useDocumentSample,
  useInsertDocument,
  useReplaceDocument,
} from '../../hooks/useDocuments';
import { documentIdParam, fieldPaths, type ServerQuery } from '../../utils/mongoQuery';
import { formatDocument } from '../../utils/extendedJson';

export const PAGE_SIZE = 20;

type Editing = { mode: 'insert' } | { mode: 'edit'; id: string; text: string } | null;

interface DocumentListProps {
  readonly collectionRef: CollectionRef;
}

export function DocumentList({ collectionRef }: DocumentListProps) {
  const [query, setQuery] = useState<ServerQuery>({});
  const [skip, setSkip] = useState(0);
  const [editing, setEditing] = useState<Editing>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const sample = useDocumentSample(collectionRef);
  const fields = useMemo(() => fieldPaths(sample.data ?? []), [sample.data]);
  const page = useDocumentPage(collectionRef, query, { limit: PAGE_SIZE, skip });
  const count = useDocumentCount(collectionRef, query.filter);
  const insert = useInsertDocument(collectionRef);
  const replace = useReplaceDocument(collectionRef);
  const remove = useDeleteDocument(collectionRef);

  const applyQuery = (next: ServerQuery) => {
    setQuery(next);
    setSkip(0);
  };

  const save = (text: string) => {
    setActionError(null);
    const done = { onSuccess: () => setEditing(null), onError: (err: unknown) => setActionError(apiErrorMessage(err)) };
    if (editing?.mode === 'edit') {
      replace.mutate({ id: editing.id, text }, done);
    } else {
      insert.mutate(text, done);
    }
  };

  const startEdit = (doc: MongoDocument) => {
    const id = documentIdParam(doc);
    if (id === null) return;
    setActionError(null);
    setEditing({ mode: 'edit', id, text: formatDocument(doc) });
  };

  const deleteDoc = (doc: MongoDocument) => {
    const id = documentIdParam(doc);
    if (id === null) return;
    setActionError(null);
    remove.mutate(id, { onError: (err) => setActionError(apiErrorMessage(err)) });
  };

  const documents = page.data?.documents ?? [];
  const total = count.data;

  return (
    <div className="space-y-4" data-testid="document-list">
      <QueryBar fields={fields} onApply={applyQuery} />

      <div className="flex items-center justify-between">
        <button
          type="button"
          onClick={() => {
            setActionError(null);
            setEditing({ mode: 'insert' });
          }}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm border border-border-primary text-text-primary hover:bg-surface-hover"
        >
          <Plus className="w-3.5 h-3.5" /> Insert document
        </button>
        <Pagination skip={skip} shown={documents.length} total={total} onSkip={setSkip} />
      </div>

      {editing && (
        <DocumentEditor
          key={editing.mode === 'edit' ? editing.id : 'insert'}
          title={editing.mode === 'edit' ? 'Edit document' : 'Insert document'}
          initialText={editing.mode === 'edit' ? editing.text : '{\n  \n}'}
          saving={insert.isPending || replace.isPending}
          serverError={actionError}
          onSave={save}
          onCancel={() => setEditing(null)}
        />
      )}
      {!editing && actionError && (
        <p role="alert" className="text-sm text-red-400">
          {actionError}
        </p>
      )}

      {page.isLoading && <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />}
      {page.isError && (
        <p role="alert" className="text-sm text-red-400">
          {apiErrorMessage(page.error)}
        </p>
      )}
      {page.isSuccess && documents.length === 0 && <p className="text-sm text-text-secondary">No documents match.</p>}

      <ul className="space-y-2">
        {documents.map((doc, index) => (
          <li
            key={documentIdParam(doc) ?? index}
            className="group rounded-lg border border-border-primary bg-surface-card p-3"
            data-testid="document-row"
          >
            <div className="flex justify-end gap-1 mb-1">
              <button
                type="button"
                aria-label="Edit document"
                onClick={() => startEdit(doc)}
                className="p-1 rounded text-text-tertiary hover:text-text-primary hover:bg-surface-hover"
              >
                <Pencil className="w-3.5 h-3.5" />
              </button>
              <InlineConfirm
                label="Delete document"
                question="Delete this document?"
                busy={remove.isPending}
                onConfirm={() => deleteDoc(doc)}
              />
            </div>
            <pre className="text-xs font-mono text-text-primary whitespace-pre-wrap break-all">
              {formatDocument(doc)}
            </pre>
          </li>
        ))}
      </ul>
    </div>
  );
}

interface PaginationProps {
  readonly skip: number;
  readonly shown: number;
  readonly total: number | undefined;
  readonly onSkip: (skip: number) => void;
}

function Pagination({ skip, shown, total, onSkip }: PaginationProps) {
  const last = skip + shown;
  const hasNext = total === undefined ? shown === PAGE_SIZE : last < total;
  const range = shown === 0 ? '0' : `${skip + 1}–${last}`;
  return (
    <div className="flex items-center gap-2 text-sm text-text-secondary">
      <span data-testid="page-range">
        {range} of {total ?? '…'}
      </span>
      <button
        type="button"
        aria-label="Previous page"
        disabled={skip === 0}
        onClick={() => onSkip(Math.max(0, skip - PAGE_SIZE))}
        className="p-1 rounded hover:bg-surface-hover disabled:opacity-40"
      >
        <ChevronLeft className="w-4 h-4" />
      </button>
      <button
        type="button"
        aria-label="Next page"
        disabled={!hasNext}
        onClick={() => onSkip(skip + PAGE_SIZE)}
        className="p-1 rounded hover:bg-surface-hover disabled:opacity-40"
      >
        <ChevronRight className="w-4 h-4" />
      </button>
    </div>
  );
}
