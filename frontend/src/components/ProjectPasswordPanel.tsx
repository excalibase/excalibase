import { useCredentials } from '../hooks/useProvisioning';
import { useProjectEndpoint } from '../api/projectEndpoint';
import { connectAddress } from './ConnectionParts';
import { OneTimePasswordPanel } from './OneTimePasswordPanel';

interface ProjectPasswordPanelProps {
  readonly projectId: string;
  readonly password: string;
  readonly onDone: () => void;
}

// The one-time panel for a project: the connection details around the
// password come from the project's own, password-free, credentials. A project
// answered while still building has none yet, so they are asked for until they
// come.
export function ProjectPasswordPanel({ projectId, password, onDone }: ProjectPasswordPanelProps) {
  const { data: credentials } = useCredentials(projectId, { untilReady: true });
  const { data: endpoint } = useProjectEndpoint(projectId);
  return (
    <OneTimePasswordPanel
      projectId={projectId}
      password={password}
      address={connectAddress(endpoint)}
      database={credentials?.databaseName ?? ''}
      user={credentials?.username ?? ''}
      sslmode={credentials?.sslMode ?? 'require'}
      pending={!credentials}
      onDone={onDone}
    />
  );
}
