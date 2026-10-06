import { cn } from '../utils/cn';

interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  readonly variant?: 'primary' | 'secondary' | 'danger' | 'ghost';
  readonly size?: 'sm' | 'md' | 'lg';
  readonly children: React.ReactNode;
}

export function Button({
  variant = 'primary',
  size = 'md',
  className,
  children,
  ...props
}: Readonly<ButtonProps>) {
  // A disabled button is dimmed and shows a not-allowed cursor, so it never
  // looks pressable.
  const baseStyles = 'font-medium rounded-lg transition-colors focus:outline-none focus:ring-2 focus:ring-accent-primary disabled:opacity-50 disabled:cursor-not-allowed';

  const variants = {
    primary: 'bg-accent-primary hover:bg-accent-primary-hover text-white',
    secondary: 'bg-surface-hover hover:bg-bg-tertiary text-text-primary border border-border-primary',
    danger: 'bg-color-error hover:bg-red-600 text-white',
    ghost: 'hover:bg-surface-hover text-text-secondary',
  };

  const sizes = {
    sm: 'px-3 py-1.5 text-sm',
    md: 'px-4 py-2',
    lg: 'px-6 py-3 text-lg',
  };

  return (
    <button
      className={cn(baseStyles, variants[variant], sizes[size], className)}
      {...props}
    >
      {children}
    </button>
  );
}
