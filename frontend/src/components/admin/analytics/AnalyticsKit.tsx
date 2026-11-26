import type { ReactNode } from 'react';
import { AlertTriangle, Loader2 } from 'lucide-react';
import { cn } from '../../../utils/cn';
import { serverErrorMessage } from '../../../utils/serverError';

export type Tone = 'neutral' | 'bad' | 'warn' | 'good';

const PILL: Record<Tone, string> = {
  neutral: 'bg-bg-secondary text-text-secondary border-border-primary',
  bad: 'bg-red-500/10 text-red-400 border-red-500/30',
  warn: 'bg-amber-500/10 text-amber-400 border-amber-500/30',
  good: 'bg-green-500/10 text-green-400 border-green-500/30',
};

export function Pill({ tone, children }: { readonly tone: Tone; readonly children: ReactNode }) {
  return <span className={cn('inline-flex px-2 py-0.5 text-xs font-medium rounded-full border whitespace-nowrap', PILL[tone])}>{children}</span>;
}

export interface Kpi {
  label: string;
  // null reads as "not collected": a dash, never a zero that could be mistaken for a measurement.
  value: number | null;
  note: string;
  tone?: Tone;
}

export function KpiRow({ items }: { readonly items: readonly Kpi[] }) {
  return (
    <dl className="grid grid-cols-2 lg:grid-cols-4 bg-surface-card border border-border-primary rounded-xl divide-border-primary">
      {items.map((item, i) => (
        <div key={item.label} className={cn('p-4 min-w-0 border-border-primary', i % 2 === 1 && 'border-l', i >= 2 && 'border-t lg:border-t-0', i >= 1 && 'lg:border-l')}>
          <dt className="text-xs uppercase tracking-wide text-text-tertiary">{item.label}</dt>
          <dd className={cn('mt-1 text-2xl font-semibold tabular-nums', item.value === null && 'text-text-tertiary', item.tone === 'bad' && item.value !== null && 'text-red-400')}>
            {item.value === null ? '—' : item.value.toLocaleString('en-US')}
          </dd>
          <div className="text-xs text-text-tertiary">{item.note}</div>
        </div>
      ))}
    </dl>
  );
}

export function Panel({ title, hint, aside, children }: { readonly title: string; readonly hint?: string; readonly aside?: ReactNode; readonly children: ReactNode }) {
  return (
    <section className="bg-surface-card border border-border-primary rounded-xl p-4 min-w-0">
      <div className="flex flex-wrap items-start justify-between gap-2 mb-3">
        <div>
          <h3 className="text-sm font-semibold text-text-primary">{title}</h3>
          {hint && <p className="text-xs text-text-tertiary">{hint}</p>}
        </div>
        {aside}
      </div>
      {children}
    </section>
  );
}

// A plain statement that something is not measured, so a blank is never read as "none".
export function Notice({ title, tone = 'warn', children }: { readonly title: string; readonly tone?: 'warn' | 'bad'; readonly children: ReactNode }) {
  return (
    <div role="note" className={cn('flex gap-3 rounded-lg border border-border-primary border-l-4 bg-surface-card p-3 text-sm', tone === 'bad' ? 'border-l-red-500' : 'border-l-amber-500')}>
      <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0 text-text-tertiary" />
      <div className="space-y-1">
        <p className="font-medium text-text-primary">{title}</p>
        <div className="text-text-secondary space-y-1">{children}</div>
      </div>
    </div>
  );
}

export function Lede({ title, children }: { readonly title: string; readonly children: ReactNode }) {
  return (
    <div>
      <h2 className="text-lg font-semibold text-text-primary">{title}</h2>
      <p className="text-sm text-text-secondary max-w-prose">{children}</p>
    </div>
  );
}

// What an analytics tab shows until its numbers arrive, or why they did not.
export function Loaded<T>({ query, children }: { readonly query: { data?: T; isLoading: boolean; error: unknown }; readonly children: (data: T) => ReactNode }) {
  if (query.isLoading) {
    return <div className="flex justify-center py-12"><Loader2 className="w-5 h-5 animate-spin text-accent-primary" /></div>;
  }
  if (query.error || !query.data) {
    return <p role="alert" className="text-sm text-red-400">{serverErrorMessage(query.error, 'The analytics could not be read')}</p>;
  }
  return <>{children(query.data)}</>;
}

// 11-26 from 2026-11-26: the axis has room for the month and day only.
export const shortDay = (day: string) => day.slice(5);
