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
