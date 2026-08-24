import { cn } from '../utils/cn';

interface CardProps {
  readonly children: React.ReactNode;
  readonly className?: string;
}

export function Card({ children, className }: CardProps) {
  return (
    <div className={cn('bg-surface-card border border-border-primary rounded-lg p-6', className)}>
      {children}
    </div>
  );
}

export function CardHeader({ children, className }: CardProps) {
  return (
    <div className={cn('mb-4', className)}>
      {children}
    </div>
  );
}

export function CardTitle({ children, className }: CardProps) {
  return (
    <h3 className={cn('text-xl font-semibold text-text-primary', className)}>
      {children}
    </h3>
  );
}

export function CardContent({ children, className }: CardProps) {
  return (
    <div className={cn('text-text-secondary', className)}>
      {children}
    </div>
  );
}
