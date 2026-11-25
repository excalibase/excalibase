import { Link } from 'react-router-dom';
import { AlertTriangle } from 'lucide-react';
import type { AttentionItem } from './model';
import { ResumeButton } from './ResumeButton';

const ACTION = 'text-purple-400 hover:underline focus-visible:underline';

// Only things that are true right now; with none, nothing is drawn.
export function AttentionBar({ items }: { readonly items: readonly AttentionItem[] }) {
  if (items.length === 0) return null;
  return (
    <section aria-label="Needs your attention" className="space-y-2 rounded-xl border border-amber-500/30 bg-amber-500/10 p-4 text-sm">
      <h2 className="flex items-center gap-2 font-semibold text-amber-400">
        <AlertTriangle className="h-4 w-4" aria-hidden="true" /> Needs your attention · {items.length}
      </h2>
      <ul className="space-y-2">
        {items.map((item) => (
          <li key={item.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 text-text-primary">
            <span>{item.before}<b>{item.subject}</b>{item.after}</span>
            {item.actions.map((action) =>
              action.resumeProjectId ? (
                <ResumeButton key={action.label} projectId={action.resumeProjectId} name={item.subject} className={ACTION} />
              ) : (
                <Link key={action.label} to={action.to ?? '/'} aria-label={`${action.label} ${item.subject}`} className={ACTION}>{action.label}</Link>
              ),
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}
