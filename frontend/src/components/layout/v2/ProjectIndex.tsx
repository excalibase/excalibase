import { lazy } from 'react';
import { useStudioV2Settling } from '../../../hooks/useDeploymentMode';
import { ServicesLoading } from '../../services/v2/ServiceList';

const ProjectOverviewPage = lazy(() => import('../../../pages/ProjectOverviewPage').then((m) => ({ default: m.ProjectOverviewPage })));
const ProjectServicesPage = lazy(() => import('../../../pages/ProjectServicesPage').then((m) => ({ default: m.ProjectServicesPage })));

// A project's first page: the services view with studio_v2 on, the overview otherwise.
export function ProjectIndex() {
  const { enabled, loading } = useStudioV2Settling();
  if (loading) return <ServicesLoading />;
  return enabled ? <ProjectServicesPage /> : <ProjectOverviewPage />;
}
