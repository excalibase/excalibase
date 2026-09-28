import type { LucideIcon } from 'lucide-react';

interface ServiceCardProps {
  readonly testId: string;
  readonly icon: LucideIcon;
  readonly title: string;
  readonly description: string;
  readonly badge?: React.ReactNode;
  readonly children: React.ReactNode;
}

// One service on the project overview. Every card owns its own loading,
// error and status, so one service's trouble never shows on another.
export function ServiceCard({
  testId,
  icon: Icon,
  title,
  description,
  badge,
  children,
}: ServiceCardProps) {
  return (
    <section
      aria-label={title}
      className="flex flex-col gap-4 bg-surface-card border border-border-primary rounded-xl p-5"
      data-testid={testId}
    >
      <header className="flex items-start justify-between gap-3">
        <div className="flex items-start gap-3">
          <span className="p-2 rounded-lg bg-purple-500/10 text-purple-400">
            <Icon className="w-5 h-5" />
          </span>
          <div>
            <h3 className="text-base font-semibold text-text-primary">{title}</h3>
            <p className="text-xs text-text-tertiary mt-0.5">{description}</p>
          </div>
        </div>
        {badge}
      </header>
      {children}
    </section>
  );
}

export function AccessNote({
  testId,
  children,
}: {
  readonly testId: string;
  readonly children: React.ReactNode;
}) {
  return (
    <p
      className="text-xs text-text-secondary border-t border-border-primary pt-3"
      data-testid={testId}
    >
      {children}
    </p>
  );
}
