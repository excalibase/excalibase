import { lazy, Suspense } from 'react';
import { Routes, Route } from 'react-router-dom';
import { AuthLayout } from './components/layout/AuthLayout';
import { AuthGuard } from './components/auth/AuthGuard';
import { VaultGuard } from './components/auth/VaultGuard';
import { PlatformLayout } from './components/layout/PlatformLayout';
import { ProjectLayout } from './components/layout/ProjectLayout';
import { DatabaseRequired } from './components/DatabaseRequired';
import { FeatureGate } from './components/FeatureGate';

// Lazy-loaded pages
const LoginPage = lazy(() => import('./pages/LoginPage').then(m => ({ default: m.LoginPage })));
const RegisterPage = lazy(() => import('./pages/RegisterPage').then(m => ({ default: m.RegisterPage })));
const VerifyEmailPage = lazy(() => import('./pages/VerifyEmailPage').then(m => ({ default: m.VerifyEmailPage })));
const OAuthCompletePage = lazy(() => import('./pages/OAuthCompletePage').then(m => ({ default: m.OAuthCompletePage })));
const ForgotPasswordPage = lazy(() => import('./pages/ForgotPasswordPage').then(m => ({ default: m.ForgotPasswordPage })));
const ResetPasswordPage = lazy(() => import('./pages/ResetPasswordPage').then(m => ({ default: m.ResetPasswordPage })));
const DashboardPage = lazy(() => import('./pages/DashboardPage').then(m => ({ default: m.DashboardPage })));
const OrgsPage = lazy(() => import('./pages/OrgsPage').then(m => ({ default: m.OrgsPage })));
const OrgDetailPage = lazy(() => import('./pages/OrgDetailPage').then(m => ({ default: m.OrgDetailPage })));
const InstancesPage = lazy(() => import('./pages/InstancesPage').then(m => ({ default: m.InstancesPage })));
const ProjectOverviewPage = lazy(() => import('./pages/ProjectOverviewPage').then(m => ({ default: m.ProjectOverviewPage })));
const AddDatabasePage = lazy(() => import('./pages/AddDatabasePage').then(m => ({ default: m.AddDatabasePage })));
const InstanceDetailPage = lazy(() => import('./pages/InstanceDetailPage').then(m => ({ default: m.InstanceDetailPage })));
const ProvisionPage = lazy(() => import('./pages/ProvisionPage').then(m => ({ default: m.ProvisionPage })));
const SqlEditorPage = lazy(() => import('./pages/SqlEditorPage').then(m => ({ default: m.SqlEditorPage })));
const TablesPage = lazy(() => import('./pages/TablesPage').then(m => ({ default: m.TablesPage })));
const TablePermissionsPage = lazy(() => import('./pages/TablePermissionsPage').then(m => ({ default: m.TablePermissionsPage })));
const FunctionsPage = lazy(() => import('./pages/FunctionsPage').then(m => ({ default: m.FunctionsPage })));
const ExtensionsPage = lazy(() => import('./pages/ExtensionsPage').then(m => ({ default: m.ExtensionsPage })));
const RolesPage = lazy(() => import('./pages/RolesPage').then(m => ({ default: m.RolesPage })));
const RlsPage = lazy(() => import('./pages/RlsPage').then(m => ({ default: m.RlsPage })));
const AuthUsersPage = lazy(() => import('./pages/AuthUsersPage').then(m => ({ default: m.AuthUsersPage })));
const AuthSessionsPage = lazy(() => import('./pages/AuthSessionsPage').then(m => ({ default: m.AuthSessionsPage })));
const EdgeFunctionsPage = lazy(() => import('./pages/EdgeFunctionsPage').then(m => ({ default: m.EdgeFunctionsPage })));
const ApiInfoPage = lazy(() => import('./pages/ApiInfoPage').then(m => ({ default: m.ApiInfoPage })));
const RealtimePage = lazy(() => import('./pages/RealtimePage').then(m => ({ default: m.RealtimePage })));
const MetricsPage = lazy(() => import('./pages/MetricsPage').then(m => ({ default: m.MetricsPage })));
const PerformancePage = lazy(() => import('./pages/PerformancePage').then(m => ({ default: m.PerformancePage })));
const AlertsPage = lazy(() => import('./pages/AlertsPage').then(m => ({ default: m.AlertsPage })));
const BackupsPage = lazy(() => import('./pages/BackupsPage').then(m => ({ default: m.BackupsPage })));
const SnapshotsPage = lazy(() => import('./pages/SnapshotsPage').then(m => ({ default: m.SnapshotsPage })));
const MigrationsPage = lazy(() => import('./pages/MigrationsPage').then(m => ({ default: m.MigrationsPage })));
const SettingsPage = lazy(() => import('./pages/SettingsPage').then(m => ({ default: m.SettingsPage })));
const VaultPage = lazy(() => import('./pages/VaultPage').then(m => ({ default: m.VaultPage })));
const SetupPage = lazy(() => import('./pages/SetupPage').then(m => ({ default: m.SetupPage })));
const SchemaDesignerPage = lazy(() => import('./pages/SchemaDesignerPage').then(m => ({ default: m.SchemaDesignerPage })));
const TriggersPage = lazy(() => import('./pages/TriggersPage').then(m => ({ default: m.TriggersPage })));
const DocumentsPage = lazy(() => import('./pages/DocumentsPage').then(m => ({ default: m.DocumentsPage })));
const MongoUsersPage = lazy(() => import('./pages/MongoUsersPage').then(m => ({ default: m.MongoUsersPage })));
const IndexesPage = lazy(() => import('./pages/IndexesPage').then(m => ({ default: m.IndexesPage })));
const TypesPage = lazy(() => import('./pages/TypesPage').then(m => ({ default: m.TypesPage })));
const AdvisorsPage = lazy(() => import('./pages/AdvisorsPage').then(m => ({ default: m.AdvisorsPage })));
const LogExplorerPage = lazy(() => import('./pages/LogExplorerPage').then(m => ({ default: m.LogExplorerPage })));
const PlatformAdminPage = lazy(() => import('./pages/PlatformAdminPage').then(m => ({ default: m.PlatformAdminPage })));
const ContainersPage = lazy(() => import('./pages/ContainersPage').then(m => ({ default: m.ContainersPage })));
const ContainerTemplatesPage = lazy(() => import('./pages/ContainerTemplatesPage').then(m => ({ default: m.ContainerTemplatesPage })));
const ContainerFormPage = lazy(() => import('./pages/ContainerFormPage').then(m => ({ default: m.ContainerFormPage })));
const ContainerDetailPage = lazy(() => import('./pages/ContainerDetailPage').then(m => ({ default: m.ContainerDetailPage })));
const ContainerPipelinePage = lazy(() => import('./pages/ContainerPipelinePage').then(m => ({ default: m.ContainerPipelinePage })));
const ConnectAiToolPage = lazy(() => import('./pages/ConnectAiToolPage').then(m => ({ default: m.ConnectAiToolPage })));
const SdkKeysPage = lazy(() => import('./pages/SdkKeysPage').then(m => ({ default: m.SdkKeysPage })));
const AccessTokensPage = lazy(() => import('./pages/AccessTokensPage').then(m => ({ default: m.AccessTokensPage })));
const StoragePage = lazy(() => import('./pages/StoragePage').then(m => ({ default: m.StoragePage })));

function PageLoader() {
  return (
    <div className="flex items-center justify-center h-full">
      <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-purple-500" />
    </div>
  );
}

export default function App() {
  return (
    <Suspense fallback={<PageLoader />}>
      <Routes>
        <Route element={<VaultGuard />}>
          {/* Vault setup wizard — own route under AuthLayout so the branded card frame is reused */}
          <Route element={<AuthLayout />}>
            <Route path="/setup" element={<SetupPage />} />
          </Route>

          {/* Auth */}
          <Route element={<AuthLayout />}>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/register" element={<RegisterPage />} />
            <Route path="/verify-email" element={<VerifyEmailPage />} />
            <Route path="/forgot-password" element={<ForgotPasswordPage />} />
            <Route path="/reset-password" element={<ResetPasswordPage />} />
            <Route path="/oauth/complete" element={<OAuthCompletePage />} />
          </Route>

          {/* Platform pages */}
          <Route element={<AuthGuard />}>
          <Route element={<PlatformLayout />}>
            <Route path="/" element={<DashboardPage />} />
            <Route path="/orgs" element={<OrgsPage />} />
            <Route path="/orgs/:orgId" element={<OrgDetailPage />} />
            <Route path="/instances" element={<InstancesPage />} />
            <Route path="/provision" element={<ProvisionPage />} />
            <Route path="/admin" element={<PlatformAdminPage />} />
            <Route path="/account/tokens" element={<AccessTokensPage />} />
          </Route>

          {/* Project-scoped pages */}
          <Route path="/project/:projectId" element={<ProjectLayout />}>
            <Route index element={<ProjectOverviewPage />} />
            <Route path="database/add" element={<AddDatabasePage />} />
            {/* Pages that work on the project's database: a project created
                without one gets a notice instead (EXC-426). */}
            <Route element={<DatabaseRequired />}>
              <Route path="sql" element={<SqlEditorPage />} />
              <Route path="schema" element={<SchemaDesignerPage />} />
              {/* Database */}
              <Route path="database/overview" element={<InstanceDetailPage />} />
              <Route path="database/tables" element={<TablesPage />} />
              <Route path="database/tables/:schema/:table/permissions" element={<TablePermissionsPage />} />
              <Route path="database/functions" element={<FunctionsPage />} />
              <Route path="database/extensions" element={<ExtensionsPage />} />
              <Route path="database/roles" element={<RolesPage />} />
              <Route path="database/rls" element={<RlsPage />} />
              <Route path="database/triggers" element={<TriggersPage />} />
              <Route path="database/indexes" element={<IndexesPage />} />
              <Route path="database/types" element={<TypesPage />} />
              <Route path="database/advisors" element={<AdvisorsPage />} />
              <Route path="database/documents" element={<DocumentsPage />} />
              <Route path="database/mongo-users" element={<MongoUsersPage />} />
              {/* Authentication: end users live in the project's database */}
              <Route path="auth/users" element={<AuthUsersPage />} />
              <Route path="auth/sessions" element={<AuthSessionsPage />} />
              <Route path="api-keys" element={<SdkKeysPage />} />
              {/* API */}
              <Route path="api" element={<ApiInfoPage />} />
              {/* Realtime */}
              <Route path="realtime" element={<RealtimePage />} />
              {/* Monitoring */}
              <Route path="monitoring/metrics" element={<MetricsPage />} />
              <Route path="monitoring/performance" element={<PerformancePage />} />
              <Route path="monitoring/logs" element={<LogExplorerPage />} />
              {/* Operations */}
              <Route path="operations/backups" element={<BackupsPage />} />
              <Route path="operations/snapshots" element={<SnapshotsPage />} />
              <Route path="operations/migrations" element={<MigrationsPage />} />
            </Route>
            {/* Containers */}
            <Route path="containers" element={<ContainersPage />} />
            <Route path="containers/new" element={<ContainerFormPage />} />
            <Route path="containers/templates" element={<ContainerTemplatesPage />} />
            <Route path="containers/:appId" element={<ContainerDetailPage />} />
            <Route path="containers/:appId/edit" element={<ContainerFormPage />} />
            <Route path="containers/:appId/pipeline" element={<FeatureGate feature="pipeline"><ContainerPipelinePage /></FeatureGate>} />
            {/* Edge Functions */}
            <Route path="edge-functions" element={<EdgeFunctionsPage />} />
            {/* Storage */}
            <Route path="storage" element={<StoragePage />} />
            <Route path="ai-tools" element={<FeatureGate feature="mcp"><ConnectAiToolPage /></FeatureGate>} />
            <Route path="monitoring/alerts" element={<AlertsPage />} />
            {/* Settings */}
            <Route path="vault" element={<VaultPage />} />
            <Route path="settings" element={<SettingsPage />} />
          </Route>
        </Route>
        </Route>
      </Routes>
    </Suspense>
  );
}
