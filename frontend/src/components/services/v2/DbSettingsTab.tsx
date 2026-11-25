import { Link } from 'react-router-dom';
import { useFeatures } from '../../../hooks/useDeploymentMode';
import { ClusterSettingsCard } from '../../ClusterSettingsCard';
import { MajorUpgradeCard } from '../../MajorUpgradeCard';
import { MinorUpgradeCard } from '../../MinorUpgradeCard';
import { isMySQLProject } from '../../../utils/engine';
import type { DatabaseInstance } from '../../../types';

// The database's own settings. Project-wide ones (allowed origins, app
// network) stay on the project's Settings page.
export function DbSettingsTab({ project }: { readonly project: DatabaseInstance }) {
  const { majorupgrade } = useFeatures();
  const postgres = !isMySQLProject(project);
  return (
    <div className="space-y-6" data-testid="db-settings">
      <MinorUpgradeCard project={project} />
      {majorupgrade && <MajorUpgradeCard project={project} />}
      {postgres && <ClusterSettingsCard project={project} />}
      <p className="text-xs text-text-tertiary">
        Allowed origins, networking between apps and the project&apos;s other settings are on the{' '}
        <Link to={`/project/${project.projectId}/settings`} className="text-purple-400 hover:underline">project settings page</Link>.
      </p>
    </div>
  );
}
