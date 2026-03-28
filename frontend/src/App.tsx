import { Routes, Route } from 'react-router-dom';
import { AppLayout } from './components/layout/AppLayout';
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

export default function App() {
  return (
    <AppLayout>
      <Routes>
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
      </Routes>
    </AppLayout>
  );
}
