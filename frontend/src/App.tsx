import { Routes, Route, Navigate } from 'react-router-dom';
import { AuthLayout } from './components/layout/AuthLayout';
import { AuthGuard } from './components/auth/AuthGuard';
import { AppLayout } from './components/layout/AppLayout';
import { ProjectLayout } from './components/layout/ProjectLayout';
import { LoginPage } from './pages/LoginPage';
import { DashboardPage } from './pages/DashboardPage';
import { InstancesPage } from './pages/InstancesPage';
import { InstanceDetailPage } from './pages/InstanceDetailPage';
import { ProvisionPage } from './pages/ProvisionPage';
import { MetricsPage } from './pages/MetricsPage';
import { BackupsPage } from './pages/BackupsPage';
import { SnapshotsPage } from './pages/SnapshotsPage';
import { PerformancePage } from './pages/PerformancePage';
import { AlertsPage } from './pages/AlertsPage';
import { MigrationsPage } from './pages/MigrationsPage';
import { ProjectsPage } from './pages/ProjectsPage';
import { SchemaDesignerPage } from './pages/SchemaDesignerPage';
import { PlaceholderPage } from './pages/PlaceholderPage';

export default function App() {
  return (
    <Routes>
      {/* Auth */}
      <Route element={<AuthLayout />}>
        <Route path="/login" element={<LoginPage />} />
      </Route>

      {/* Protected */}
      <Route element={<AuthGuard />}>
        <Route element={<AppLayout />}>
          <Route path="/" element={<DashboardPage />} />
          <Route path="/instances" element={<InstancesPage />} />
          <Route path="/instances/:projectId" element={<InstanceDetailPage />} />
          <Route path="/provision" element={<ProvisionPage />} />
          <Route path="/metrics" element={<MetricsPage />} />
          <Route path="/backups" element={<BackupsPage />} />
          <Route path="/snapshots" element={<SnapshotsPage />} />
          <Route path="/performance" element={<PerformancePage />} />
          <Route path="/alerts" element={<AlertsPage />} />
          <Route path="/migrations" element={<MigrationsPage />} />
          <Route path="/projects" element={<ProjectsPage />} />
          <Route path="/projects/:projectId" element={<ProjectLayout />}>
            <Route index element={<Navigate to="schema" replace />} />
            <Route path="schema" element={<SchemaDesignerPage />} />
            <Route path="sql" element={<PlaceholderPage title="SQL Editor" description="Execute SQL queries against your database." />} />
            <Route path="functions" element={<PlaceholderPage title="Functions" description="Manage database functions and stored procedures." />} />
            <Route path="rls" element={<PlaceholderPage title="Row-Level Security" description="Configure row-level security policies." />} />
            <Route path="backups" element={<PlaceholderPage title="Backups" description="Manage project backups and restore points." />} />
            <Route path="roles" element={<PlaceholderPage title="Roles" description="Manage database roles and permissions." />} />
            <Route path="extensions" element={<PlaceholderPage title="Extensions" description="Install and manage database extensions." />} />
            <Route path="settings" element={<PlaceholderPage title="Settings" description="Configure project settings." />} />
          </Route>
        </Route>
      </Route>
    </Routes>
  );
}
