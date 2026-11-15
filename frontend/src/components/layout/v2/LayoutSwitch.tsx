import { PlatformLayout } from '../PlatformLayout';
import { ProjectLayout } from '../ProjectLayout';
import { ProjectShell } from './ProjectShell';
import { StudioShell } from './StudioShell';
import { useStudioV2 } from '../../../hooks/useDeploymentMode';

// The studio_v2 flag picks the layout. Until the server answers, or when it fails to, the flag counts as off: the current layout, never a blocked page.
export function PlatformLayoutSwitch() {
  const { enabled } = useStudioV2();
  return enabled ? <StudioShell /> : <PlatformLayout />;
}

export function ProjectLayoutSwitch() {
  const { enabled } = useStudioV2();
  return enabled ? <ProjectShell /> : <ProjectLayout />;
}
