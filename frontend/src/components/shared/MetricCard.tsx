import { cn } from '../../utils/cn';

interface MetricCardProps {
  readonly icon: React.ReactNode;
  readonly label: string;
  readonly value: string | number;
  readonly subtitle?: string;
  readonly color?: string;
  readonly className?: string;
}

export function MetricCard({ icon, label, value, subtitle, color = 'text-accent-primary', className }: MetricCardProps) {
  return (
    <div className={cn('bg-surface-card border border-border-primary rounded-xl p-5', className)}>
      <div className="flex items-center gap-3">
        <div className={cn('w-10 h-10 rounded-lg flex items-center justify-center bg-bg-tertiary', color)}>
          {icon}
        </div>
        <div>
          <p className="text-xs text-text-tertiary">{label}</p>
          <p className="text-2xl font-bold text-text-primary leading-tight">{value}</p>
          {subtitle && <p className="text-xs text-text-secondary mt-0.5">{subtitle}</p>}
        </div>
      </div>
    </div>
  );
}
