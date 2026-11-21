import { useEffect, useRef, useState } from 'react';
import { Columns3 } from 'lucide-react';
import type { Column } from '../DataTable';

export function ColumnChooser<T>({ columns, hidden, onToggle }: { readonly columns: readonly Column<T>[]; readonly hidden: readonly string[]; readonly onToggle: (id: string) => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false); };
    const escape = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', escape);
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', escape); };
  }, [open]);

  return (
    <div ref={ref} className="relative">
      <button type="button" aria-haspopup="true" aria-expanded={open} onClick={() => setOpen((o) => !o)}
        className="inline-flex items-center gap-1.5 rounded-lg border border-border-primary bg-bg-secondary px-3 min-h-9 text-sm text-text-primary hover:bg-surface-hover">
        <Columns3 className="w-4 h-4" aria-hidden="true" /> Columns
      </button>
      {open && (
        <fieldset className="absolute right-0 top-full z-20 mt-1 w-52 rounded-xl border border-border-primary bg-surface-card p-2 shadow-lg">
          <legend className="sr-only">Visible columns</legend>
          {columns.map((c) => (
            <label key={c.id} className="flex items-center gap-2 rounded px-2 py-1.5 text-sm text-text-primary hover:bg-surface-hover">
              <input type="checkbox" checked={!hidden.includes(c.id)} disabled={c.locked} onChange={() => onToggle(c.id)} />
              {c.header}
            </label>
          ))}
        </fieldset>
      )}
    </div>
  );
}
