import { useParams } from 'react-router-dom';

// The project a project page acts on is the one in its URL (/project/:projectId/...),
// never a selection kept elsewhere: a header or a stale selection must not
// re-target a backup, a restore or a migration at another project.
export function useRouteProjectId(): string {
  const { projectId } = useParams<{ projectId: string }>();
  return projectId ?? '';
}
