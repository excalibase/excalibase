import { Link } from 'react-router-dom';

// Said where the install has no static hosting, so a person looking for it is pointed at containers.
export function SitesUnavailable({ projectId }: { readonly projectId: string }) {
  return (
    <p className="text-xs text-text-tertiary" data-testid="sites-unavailable">
      Static frontend hosting isn&apos;t available on this install; deploy your frontend as a{' '}
      <Link to={`/project/${projectId}/containers`} className="text-purple-400 hover:underline">
        container
      </Link>
      .
    </p>
  );
}
