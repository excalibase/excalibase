export interface TableInfo {
  name: string;
  schema: string;
  type: string;
  comment: string | null;
}

export interface ColumnInfo {
  name: string;
  dataType: string;
  nullable: boolean;
  primaryKey: boolean;
  unique: boolean;
  defaultValue: string | null;
  ordinalPosition: number;
  comment: string | null;
  characterMaximumLength: number | null;
  numericPrecision: number | null;
  numericScale: number | null;
}

export interface RelationshipInfo {
  constraintName: string;
  sourceTable: string;
  sourceColumn: string;
  targetTable: string;
  targetColumn: string;
  onDelete: string;
  onUpdate: string;
}

export interface TablePosition {
  id: number;
  projectId: number;
  tableName: string;
  schemaName: string;
  positionX: number;
  positionY: number;
}

// Query execution
export interface ColumnMeta {
  name: string;
  dataType: string;
}

export interface QueryResult {
  columns?: ColumnMeta[];
  rows?: unknown[][];
  command?: string;
  affectedRows?: number;
  error?: string;
}

// Roles
export interface RoleInfo {
  name: string;
  login: boolean;
  superuser: boolean;
  createDb: boolean;
  createRole: boolean;
  connLimit: number;
}

// Extensions
export interface ExtensionInfo {
  name: string;
  installedVersion: string | null;
  defaultVersion: string;
  schema: string | null;
  comment: string | null;
}

// Policies (RLS)
export interface PolicyInfo {
  name: string;
  table: string;
  command: string;
  roles: string;
  using: string | null;
  withCheck: string | null;
  permissive: boolean;
}

// Functions
export interface FunctionInfo {
  name: string;
  schema: string;
  language: string;
  returnType: string;
  argTypes: string;
  volatility: string;
  definition: string;
}

// Triggers
export interface TriggerInfo {
  name: string;
  table: string;
  schema: string;
  event: string;
  timing: string;
  forEachRow: boolean;
  function: string;
  enabled: boolean;
}

// Indexes (extended)
export interface IndexInfo {
  name: string;
  tableName: string;
  columns: string[];
  unique: boolean;
  type: string;
}

// PG Types
export interface PgTypeInfo {
  name: string;
  schema: string;
  type: string;
  values: string[];
}

// Row data
export interface RowsResult {
  columns: ColumnMeta[];
  rows: unknown[][];
  totalCount: number;
}

// Advisor
export interface AdvisorFinding {
  ruleId: string;
  severity: string;
  category: string;
  title: string;
  description: string;
  table?: string;
  detail?: string;
  fix?: string;
}
