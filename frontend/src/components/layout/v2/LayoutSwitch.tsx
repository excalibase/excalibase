import { Loader2 } from 'lucide-react';
import { PlatformLayout } from '../PlatformLayout';
import { ProjectLayout } from '../ProjectLayout';
import { ProjectShell } from './ProjectShell';
import { StudioShell } from './StudioShell';
import { useStudioV2 } from '../../../hooks/useDeploymentMode';

function Waiting() {
  return (
    <div className="flex h-screen items-center justify-center" role="status" aria-label="Loading">
      <Loader2 className="w-6 h-6 animate-spin text-text-tertiary" />
    </div>
  );
}

// The studio_v2 flag picks the layout; off (or not yet answered) never shows the new one.
export function PlatformLayoutSwitch() {
  const { enabled, isLoading } = useStudioV2();
  if (isLoading) return <Waiting />;
  return enabled ? <StudioShell /> : <PlatformLayout />;
}

export function ProjectLayoutSwitch() {
  const { enabled, isLoading } = useStudioV2();
  if (isLoading) return <Waiting />;
  return enabled ? <ProjectShell /> : <ProjectLayout />;
}
