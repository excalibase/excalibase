import { lazy, Suspense, useRef } from 'react';
import { PlatformLayout } from '../PlatformLayout';
import { ProjectLayout } from '../ProjectLayout';
import { LayoutLoading } from './LayoutLoading';
import { useStudioV2 } from '../../../hooks/useDeploymentMode';

const StudioShell = lazy(() => import('./StudioShell').then((m) => ({ default: m.StudioShell })));
const ProjectShell = lazy(() => import('./ProjectShell').then((m) => ({ default: m.ProjectShell })));

// Loading counts only until the flag is first known: a failed request is retried when the layout mounts, and that
// retry must not swap the layout for the loader again.
function useLayoutFlag(): { enabled: boolean; loading: boolean } {
  const { enabled, isLoading } = useStudioV2();
  const settled = useRef(false);
  if (!isLoading) settled.current = true;
  return { enabled, loading: isLoading && !settled.current };
}

// The studio_v2 flag picks the layout. While the server has not answered a neutral loader shows, so the old layout never mounts first;
// if the request fails the flag counts as off: the current layout, never a blocked page.
export function PlatformLayoutSwitch() {
  const { enabled, loading } = useLayoutFlag();
  if (loading) return <LayoutLoading />;
  return enabled ? <Suspense fallback={<LayoutLoading />}><StudioShell /></Suspense> : <PlatformLayout />;
}

export function ProjectLayoutSwitch() {
  const { enabled, loading } = useLayoutFlag();
  if (loading) return <LayoutLoading />;
  return enabled ? <Suspense fallback={<LayoutLoading />}><ProjectShell /></Suspense> : <ProjectLayout />;
}
