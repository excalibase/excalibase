import { Loader2 } from 'lucide-react';
import { useFeatureEnabled, type Feature } from '../hooks/useDeploymentMode';

// A page of a feature that ships dark (EXC-554): opened by its URL while the
// feature is off, it explains instead of calling routes the server hides.
export function FeatureGate({
  feature,
  children,
}: {
  readonly feature: Feature;
  readonly children: React.ReactNode;
}) {
  const { enabled, isLoading } = useFeatureEnabled(feature);
  if (isLoading) {
    return (
      <div className="flex justify-center py-12">
        <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />
      </div>
    );
  }
  if (!enabled) {
    return (
      <div className="text-center py-12" data-testid="feature-unavailable">
        <p className="text-sm text-text-primary">
          This page is not available on this installation.
        </p>
        <p className="text-xs text-text-tertiary mt-1">An administrator can turn the feature on.</p>
      </div>
    );
  }
  return <>{children}</>;
}
