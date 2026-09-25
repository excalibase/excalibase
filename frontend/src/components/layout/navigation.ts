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
  type LucideIcon,
} from 'lucide-react';

export interface NavItem {
  label: string;
  icon: LucideIcon;
  to: string;
  // Shown only for projects created with DocumentDB.
  documentDbOnly?: boolean;
}

export interface NavSection {
  key: string;
  label: string;
  icon: LucideIcon;
  to?: string;
  children?: NavItem[];
}

export const PROJECT_NAV: NavSection[] = [
  { key: 'home', label: 'Home', icon: Home, to: '' },
  { key: 'sql', label: 'SQL Editor', icon: Terminal, to: 'sql' },
  { key: 'schema', label: 'Schema', icon: GitBranch, to: 'schema' },
  {
    key: 'database',
    label: 'Database',
    icon: Database,
    children: [
      { label: 'Tables', icon: Table2, to: 'database/tables' },
      { label: 'Functions', icon: Code, to: 'database/functions' },
      { label: 'Extensions', icon: Package, to: 'database/extensions' },
      { label: 'Roles', icon: Users, to: 'database/roles' },
      { label: 'RLS Policies', icon: Shield, to: 'database/rls' },
      { label: 'Triggers', icon: Zap, to: 'database/triggers' },
      { label: 'Indexes', icon: List, to: 'database/indexes' },
      { label: 'Types', icon: Hash, to: 'database/types' },
      { label: 'Advisors', icon: AlertTriangle, to: 'database/advisors' },
      { label: 'Documents', icon: FileJson, to: 'database/documents', documentDbOnly: true },
    ],
  },
  {
    key: 'auth',
    label: 'Authentication',
    icon: Lock,
    children: [
      { label: 'Users', icon: Users, to: 'auth/users' },
      { label: 'Sessions', icon: KeyRound, to: 'auth/sessions' },
    ],
  },
  // Temporarily hidden — pending integration with excalibase-rest/watcher/serverless
  // { key: 'edge-functions', label: 'Edge Functions', icon: Code2, to: 'edge-functions' },
  // { key: 'api', label: 'API', icon: Globe, to: 'api' },
  // { key: 'realtime', label: 'Realtime', icon: Radio, to: 'realtime' },
  { key: 'storage', label: 'Storage', icon: FolderOpen, to: 'storage' },
  {
    key: 'monitoring',
    label: 'Monitoring',
    icon: Activity,
    children: [
      { label: 'Metrics', icon: BarChart2, to: 'monitoring/metrics' },
      { label: 'Performance', icon: Zap, to: 'monitoring/performance' },
      { label: 'Alerts', icon: Bell, to: 'monitoring/alerts' },
      { label: 'Logs', icon: FileText, to: 'monitoring/logs' },
    ],
  },
  {
    key: 'operations',
    label: 'Operations',
    icon: Wrench,
    children: [
      { label: 'Backups', icon: HardDrive, to: 'operations/backups' },
      { label: 'Snapshots', icon: Camera, to: 'operations/snapshots' },
      { label: 'Migrations', icon: ArrowUpDown, to: 'operations/migrations' },
    ],
  },
  { key: 'vault', label: 'Vault', icon: KeyRound, to: 'vault' },
  { key: 'settings', label: 'Settings', icon: Settings, to: 'settings' },
];

export function visibleNavItems(items: readonly NavItem[], documentDb: boolean): NavItem[] {
  return items.filter((item) => !item.documentDbOnly || documentDb);
}
