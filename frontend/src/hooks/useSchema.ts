// Barrel re-export — all schema hooks split into focused files
export { useExecuteQuery } from './useSchemaQuery';
export { useTables, useColumns, useRelationships, useCreateTable, useUpdateTable, useDropTable, useAddColumn, useAlterColumn, useDropColumn } from './useSchemaTables';
export { useRoles, useCreateRole, useDropRole, useExtensions, useCreateExtension, useDropExtension, usePolicies, useCreatePolicy, useDropPolicy, useFunctions, useCreateFunction, useDropFunction, useTriggers, useCreateTrigger, useDropTrigger, useIndexes, useCreateIndex, useDropIndex, usePgTypes } from './useSchemaObjects';
export { useRows, useInsertRow, useUpdateRow, useDeleteRow } from './useSchemaData';
export { usePerformanceAdvisor, useSecurityAdvisor } from './useSchemaAdvisors';
