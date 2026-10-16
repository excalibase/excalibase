import {
  Home,
  Terminal,
  GitBranch,
  Database,
  Lock,
  Activity,
  Wrench,
  Settings,
  Table2,
  Code,
  Package,
  Users,
  Shield,
  KeyRound,
  BarChart2,
  Zap,
  Bell,
  HardDrive,
  Camera,
  ArrowUpDown,
  List,
  Hash,
  AlertTriangle,
  FileText,
  FolderOpen,
  FileJson,
  UserCog,
  Container,
  Globe,
  LayoutDashboard,
  Bot,
  type LucideIcon,
} from 'lucide-react';

export interface NavItem {
  label: string;
  icon: LucideIcon;
  to: string;
  // Shown only for projects created with DocumentDB.
  documentDbOnly?: boolean;
  // Works only on a PostgreSQL database; hidden for MySQL projects (ADR 0041).
  postgresOnly?: boolean;
  // Hidden for MySQL projects, and this one line says why when it is opened.
  mysqlUnavailable?: string;
}

export interface NavSection {
  key: string;
  label: string;
  icon: LucideIcon;
  to?: string;
  children?: NavItem[];
  // Shown only when the server reports the capability as enabled.
  requires?: keyof NavCapabilities;
  // Works only on a PostgreSQL database; hidden for MySQL projects (ADR 0041).
  postgresOnly?: boolean;
}

export interface NavCapabilities {
  appHosting: boolean;
  // MCP for coding tools ships dark (EXC-554).
  mcp: boolean;
  // Static frontend hosting, only where the server reports it (EXC-353).
  sites?: boolean;
  // False for a MySQL project: the Postgres-only pages are not listed.
  postgres?: boolean;
}

// Why a database page is not offered on a MySQL project (ADR 0041 decision 9).
const MYSQL_REASONS = {
  overview: 'The database overview reads Postgres metrics and logs; the project overview and Settings show this MySQL database.',
  extensions: 'MySQL has no extensions; its features ship built in.',
  roles: 'MySQL users are created and rotated by the platform.',
  rls: "CREATE POLICY is Postgres row-level security; on MySQL, API access is set in each table's permissions.",
  triggers: "Studio's trigger editor builds Postgres trigger functions; create MySQL triggers in the SQL editor.",
  types: 'MySQL has no user-defined types; ENUM and SET are column types.',
  advisors: 'The advisors read Postgres statistics catalogs.',
  documentDb: 'DocumentDB runs on Postgres.',
} as const;

export const PROJECT_NAV: NavSection[] = [
  { key: 'home', label: 'Overview', icon: Home, to: '' },
  { key: 'sql', label: 'SQL Editor', icon: Terminal, to: 'sql' },
  { key: 'schema', label: 'Schema', icon: GitBranch, to: 'schema' },
  {
    key: 'database',
    label: 'Databases',
    icon: Database,
    children: [
      { label: 'Overview', icon: LayoutDashboard, to: 'database/overview', mysqlUnavailable: MYSQL_REASONS.overview },
      { label: 'Tables', icon: Table2, to: 'database/tables' },
      { label: 'Functions', icon: Code, to: 'database/functions' },
      { label: 'Extensions', icon: Package, to: 'database/extensions', mysqlUnavailable: MYSQL_REASONS.extensions },
      { label: 'Roles', icon: Users, to: 'database/roles', mysqlUnavailable: MYSQL_REASONS.roles },
      { label: 'RLS Policies', icon: Shield, to: 'database/rls', mysqlUnavailable: MYSQL_REASONS.rls },
      { label: 'Triggers', icon: Zap, to: 'database/triggers', mysqlUnavailable: MYSQL_REASONS.triggers },
      { label: 'Indexes', icon: List, to: 'database/indexes' },
      { label: 'Types', icon: Hash, to: 'database/types', mysqlUnavailable: MYSQL_REASONS.types },
      { label: 'Advisors', icon: AlertTriangle, to: 'database/advisors', mysqlUnavailable: MYSQL_REASONS.advisors },
      { label: 'Documents', icon: FileJson, to: 'database/documents', documentDbOnly: true, mysqlUnavailable: MYSQL_REASONS.documentDb },
      { label: 'Mongo Users', icon: UserCog, to: 'database/mongo-users', documentDbOnly: true, mysqlUnavailable: MYSQL_REASONS.documentDb },
    ],
  },
  { key: 'containers', label: 'Containers', icon: Container, to: 'containers', requires: 'appHosting' },
  { key: 'sites', label: 'Sites', icon: Globe, to: 'sites', requires: 'sites' },
  {
    key: 'auth',
    postgresOnly: true,
    label: 'Authentication',
    icon: Lock,
    children: [
      { label: 'Users', icon: Users, to: 'auth/users' },
      { label: 'Sessions', icon: KeyRound, to: 'auth/sessions' },
      { label: 'Settings', icon: Settings, to: 'auth/settings' },
    ],
  },
  // Temporarily hidden — pending integration with excalibase-rest/watcher/serverless
  // { key: 'edge-functions', label: 'Edge Functions', icon: Code2, to: 'edge-functions' },
  // { key: 'api', label: 'API', icon: Globe, to: 'api' },
  // { key: 'realtime', label: 'Realtime', icon: Radio, to: 'realtime' },
  { key: 'storage', label: 'Storage', icon: FolderOpen, to: 'storage' },
  { key: 'api-keys', label: 'API Keys', icon: KeyRound, to: 'api-keys', postgresOnly: true },
  { key: 'ai-tools', label: 'AI Tools', icon: Bot, to: 'ai-tools', requires: 'mcp' },
  {
    key: 'monitoring',
    label: 'Monitoring',
    icon: Activity,
    children: [
      { label: 'Metrics', icon: BarChart2, to: 'monitoring/metrics', postgresOnly: true },
      { label: 'Performance', icon: Zap, to: 'monitoring/performance', postgresOnly: true },
      { label: 'Alerts', icon: Bell, to: 'monitoring/alerts' },
      { label: 'Logs', icon: FileText, to: 'monitoring/logs', postgresOnly: true },
    ],
  },
  {
    key: 'operations',
    label: 'Operations',
    icon: Wrench,
    children: [
      // MySQL projects are backed up and restored too (EXC-586).
      { label: 'Backups', icon: HardDrive, to: 'operations/backups' },
      { label: 'Snapshots', icon: Camera, to: 'operations/snapshots', postgresOnly: true },
      { label: 'Migrations', icon: ArrowUpDown, to: 'operations/migrations', postgresOnly: true },
    ],
  },
  { key: 'vault', label: 'Vault', icon: KeyRound, to: 'vault' },
  { key: 'settings', label: 'Settings', icon: Settings, to: 'settings' },
];

export function visibleNavItems(items: readonly NavItem[], documentDb: boolean, postgres = true): NavItem[] {
  return items.filter(
    (item) => (!item.documentDbOnly || documentDb) && (postgres || (!item.postgresOnly && !item.mysqlUnavailable))
  );
}

// The reason a page is hidden on MySQL, by its path below the project.
export function mysqlUnavailableReason(path: string): string | undefined {
  return PROJECT_NAV.flatMap((section) => section.children ?? []).find((item) => item.to === path)?.mysqlUnavailable;
}

export const visibleProjectNav = (capabilities: NavCapabilities): NavSection[] =>
  PROJECT_NAV.filter(
    (section) =>
      (!section.requires || capabilities[section.requires]) &&
      (!section.postgresOnly || capabilities.postgres !== false)
  );
