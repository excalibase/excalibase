// Use const objects instead of enums for erasableSyntaxOnly compatibility
export const DatabaseType = {
  POSTGRESQL: 'POSTGRESQL',
  MYSQL: 'MYSQL',
  MONGODB: 'MONGODB'
} as const;

export type DatabaseType = typeof DatabaseType[keyof typeof DatabaseType];

export const TierType = {
  FREE: 'FREE',
  STANDARD: 'STANDARD',
  ENTERPRISE: 'ENTERPRISE'
} as const;

export type TierType = typeof TierType[keyof typeof TierType];

export const ProvisioningStage = {
  VALIDATING: 'VALIDATING',
  NAMESPACE_CREATION: 'NAMESPACE_CREATION',
  CRD_DEPLOYMENT: 'CRD_DEPLOYMENT',
  WAITING_FOR_READY: 'WAITING_FOR_READY',
  CREDENTIAL_GENERATION: 'CREDENTIAL_GENERATION',
  BACKUP_CONFIGURATION: 'BACKUP_CONFIGURATION',
  METRICS_SETUP: 'METRICS_SETUP',
  WATCHER_DEPLOYMENT: 'WATCHER_DEPLOYMENT',
  ROLE_CREATION: 'ROLE_CREATION',
  COMPLETED: 'COMPLETED',
  FAILED: 'FAILED'
} as const;

export type ProvisioningStage = typeof ProvisioningStage[keyof typeof ProvisioningStage];

export interface ProvisioningRequest {
  projectName: string;
  orgId: string;
  databaseType: DatabaseType;
  tier: TierType;
}

export interface DatabaseInstance {
  id: number;
  projectId: string;
  projectName?: string;
  orgId: string;
  databaseType: DatabaseType;
  tier: TierType;
  namespace: string;
  host: string;
  port: number;
  databaseName: string;
  username: string;
  password: string;
  status: string;
  currentStage: ProvisioningStage;
  currentStep?: string;
  failureReason?: string;
  failureStage?: ProvisioningStage;
  failureStep?: string;
  rollbackLog?: string;
  backupEnabled: boolean;
  backupSchedule?: string;
  backupRetentionDays?: number;
  metricsEndpoint?: string;
  createdAt: string;
  updatedAt: string;
  lastHealthCheck?: string;
  grafanaDashboardUrl?: string;
  // Pause-related (set when project is PAUSED or has been tracked)
  deploymentMode?: 'k8s' | 'docker' | 'byoc';
  lastActiveAt?: string;
  pauseReason?: 'idle_7d' | 'manual' | 'tier_limit' | '';
  // Last successful project-scoped call seen by the platform (project_activity); absent when never seen.
  lastSeenAt?: string;
  // Set while an idle-pause warning is outstanding (cleared by fresh activity).
  idleWarnedAt?: string;
}

export interface CredentialsResponse {
  projectId: string;
  host: string;
  port: number;
  databaseName: string;
  username: string;
  password: string;
  connectionString: string;
}

export interface BackupConfig {
  schedule: string;
  retention: number;
}

export interface BackupInfo {
  id: string;
  timestamp: string;
  size: string;
  status: string;
  type?: string;
}

export interface PodMetrics {
  name: string;
  role: 'primary' | 'replica';
  cpuCores: number;
  cpuLimitCores: number;
  memoryMB: number;
  memoryLimitMB: number;
}

export interface DatabaseMetrics {
  projectId: string;
  timestamp: string;
  status: string;
  healthStatus: string; // HEALTHY, DEGRADED, DOWN
  metricsAvailable: boolean;
  unavailableReason: string | null;

  // Resource usage (from metrics-server)
  cpuUsagePercent: number | null;
  memoryUsagePercent: number | null;
  diskUsagePercent: number | null;
  cpuUsageCores: number | null;
  memoryUsageMB: number | null;
  diskUsageGB: number | null;

  // Resource limits (from tier config)
  cpuLimitCores: number | null;
  memoryLimitMB: number | null;
  storageLimit: string | null;
  instanceCount: number | null;

  // Database metrics (from CNPG port 9187)
  activeConnections: number | null;
  idleConnections: number | null;
  maxConnections: number | null;
  queriesPerSecond: number | null;
  averageQueryLatencyMs: number | null;
  slowQueryCount: number | null;
  databaseSizeGB: number | null;

  // Backup info
  lastBackupTime?: string;
  nextBackupTime?: string;

  // Per-pod breakdown (from metrics-server)
  pods?: PodMetrics[];
}

export interface MetricsHistory {
  projectId: string;
  metrics: DatabaseMetrics[];
  totalPoints: number;
}
