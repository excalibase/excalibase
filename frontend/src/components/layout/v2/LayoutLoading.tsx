import { Loader2 } from 'lucide-react';

// Shown while the server has not yet said which layout to use, so the old one never flashes first.
export function LayoutLoading() {
  return (
    <output aria-label="Loading" data-testid="layout-loading" className="flex h-screen items-center justify-center bg-bg-primary">
      <Loader2 className="h-6 w-6 animate-spin text-text-tertiary" aria-hidden="true" />
    </output>
  );
}
