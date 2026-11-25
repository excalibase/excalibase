import { useProjectEndpoint } from '../../../api/projectEndpoint';
import { ConnectionStrings } from '../../ConnectionStrings';
import { ConnectSnippets } from '../../ConnectSnippets';
import { PublicPortCard } from '../../PublicPortCard';
import { ReadAddressCard } from '../../ReadAddressCard';
import { isMySQLProject } from '../../../utils/engine';
import type { DatabaseInstance } from '../../../types';

// How to reach the database. These are the components the project's Settings
// page uses, so what they show and withhold is the same: no cluster address,
// no stored password (it is shown once, when it is set or reset), and a public
// port that only the allowed IP addresses can use.
export function DbConnectTab({ project }: { readonly project: DatabaseInstance }) {
  const endpoint = useProjectEndpoint(project.projectId);
  const mysql = isMySQLProject(project);
  return (
    <div className="space-y-6" data-testid="db-connect">
      {!mysql && (
        <div className="rounded-lg border border-border-primary bg-bg-secondary p-4" data-testid="connect-section">
          <h3 className="mb-3 text-sm font-medium text-text-primary">Connect to your project</h3>
          <ConnectSnippets projectId={project.projectId} />
        </div>
      )}
      <ConnectionStrings projectId={project.projectId} documentDb={!mysql && project.documentDb === true} endpoint={endpoint.data} />
      <PublicPortCard projectId={project.projectId} status={project.status} />
      <div className="empty:hidden">
        <ReadAddressCard projectId={project.projectId} status={project.status} />
      </div>
    </div>
  );
}
