import { cn } from '../../utils/cn';
import { MONITORING_RANGES, type MonitoringRange } from '../../api/monitoring';

export function RangePicker({
  value,
  onChange,
}: {
  readonly value: MonitoringRange;
  readonly onChange: (next: MonitoringRange) => void;
}) {
  return (
    <div className="inline-flex rounded-lg border border-border-primary overflow-hidden">
      {MONITORING_RANGES.map((option) => (
        <button
          key={option}
          type="button"
          aria-pressed={option === value}
          onClick={() => onChange(option)}
          className={cn(
            'px-3 py-1.5 text-xs font-medium',
            option === value
              ? 'bg-accent-primary/20 text-text-primary'
              : 'text-text-secondary hover:bg-surface-hover',
          )}
        >
          {option}
        </button>
      ))}
    </div>
  );
}
