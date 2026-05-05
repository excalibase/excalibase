interface SkeletonProps {
  readonly className?: string;
}

export function Skeleton({ className = '' }: SkeletonProps) {
  return <div className={`animate-pulse bg-bg-secondary rounded ${className}`} />;
}

export function SkeletonTable({ rows = 5, cols = 4 }: { readonly rows?: number; readonly cols?: number }) {
  return (
    <div className="space-y-3" data-testid="skeleton-table">
      {/* Header */}
      <div className="flex gap-4">
        {Array.from({ length: cols }).map((_, i) => (
          // Static skeleton placeholders have no stable id; index is acceptable
          // here because this list is purely decorative and never reordered.
          // eslint-disable-next-line react/no-array-index-key
          <Skeleton key={`hdr-${i}`} className="h-4 flex-1" />
        ))}
      </div>
      {/* Rows */}
      {Array.from({ length: rows }).map((_, ri) => (
        // eslint-disable-next-line react/no-array-index-key
        <div key={`row-${ri}`} className="flex gap-4">
          {Array.from({ length: cols }).map((_, ci) => (
            // eslint-disable-next-line react/no-array-index-key
            <Skeleton key={`cell-${ri}-${ci}`} className="h-8 flex-1" />
          ))}
        </div>
      ))}
    </div>
  );
}

export function SkeletonCard() {
  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4 space-y-3" data-testid="skeleton-card">
      <Skeleton className="h-4 w-1/3" />
      <Skeleton className="h-3 w-2/3" />
      <Skeleton className="h-3 w-1/2" />
    </div>
  );
}
