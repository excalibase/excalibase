interface SqlEngineNoteProps {
  readonly mysql: boolean;
}

// Names the dialect the editor highlights and completes, and on MySQL warns
// that a failed script leaves the statements before it applied (ADR 0041).
export function SqlEngineNote({ mysql }: SqlEngineNoteProps) {
  return (
    <div className="flex items-center gap-2 text-[11px] text-text-tertiary">
      <span data-testid="sql-dialect" className="px-1.5 py-0.5 rounded border border-border-primary">
        {mysql ? 'MySQL' : 'PostgreSQL'}
      </span>
      {mysql && (
        <span data-testid="sql-mysql-ddl-note" className="text-amber-400">
          MySQL DDL is not transactional: a script that fails part-way is not rolled back.
        </span>
      )}
    </div>
  );
}
