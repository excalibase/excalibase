// Building blocks the database panel's Data-side tabs share, so Backups,
// Metrics and Logs read the same as Overview.
export const card = 'space-y-3 rounded-lg border border-border-primary bg-bg-secondary p-4';

export function Section({ id, title, aside, children }: { readonly id: string; readonly title: string; readonly aside?: React.ReactNode; readonly children: React.ReactNode }) {
  return (
    <section aria-labelledby={id} className={card}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 id={id} className="text-sm font-semibold text-text-primary">{title}</h3>
        {aside}
      </div>
      {children}
    </section>
  );
}

export function Fact({ label, children }: { readonly label: string; readonly children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-text-tertiary">{label}</dt>
      <dd className="break-words text-sm text-text-primary">{children}</dd>
    </div>
  );
}

export function Notice({ children, testId }: { readonly children: React.ReactNode; readonly testId?: string }) {
  return <p data-testid={testId} className="rounded-lg border border-border-primary bg-bg-secondary px-4 py-3 text-sm text-text-secondary">{children}</p>;
}
