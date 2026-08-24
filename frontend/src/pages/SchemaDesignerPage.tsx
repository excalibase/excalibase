import { useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Loader2, Table2 } from 'lucide-react';
import { api } from '../api/client';
import { SchemaCanvas } from '../components/schema/SchemaCanvas';
import type { TableInfo, ColumnInfo, RelationshipInfo } from '../types/schema';

export function SchemaDesignerPage() {
  const { projectId } = useParams<{ projectId: string }>();

  const {
    data: tables,
    isLoading: tablesLoading,
    error: tablesError,
  } = useQuery({
    queryKey: ['schema-tables', projectId],
    queryFn: async () => {
      const response = await api.get<TableInfo[]>(`/schema/${projectId}/tables`);
      return response.data;
    },
    enabled: !!projectId,
  });

  const {
    data: relationships,
    isLoading: relsLoading,
  } = useQuery({
    queryKey: ['schema-relationships', projectId],
    queryFn: async () => {
      const response = await api.get<RelationshipInfo[]>(`/schema/${projectId}/relationships`);
      return response.data;
    },
    enabled: !!projectId,
  });

  const {
    data: columnsMap,
    isLoading: columnsLoading,
  } = useQuery({
    queryKey: ['schema-columns', projectId, tables?.map((t) => t.name)],
    queryFn: async () => {
      if (!tables) return {};
      const entries = await Promise.all(
        tables.map(async (table) => {
          const response = await api.get<ColumnInfo[]>(
            `/schema/${projectId}/tables/${table.name}/columns`
          );
          return [table.name, response.data] as const;
        })
      );
      return Object.fromEntries(entries) as Record<string, ColumnInfo[]>;
    },
    enabled: !!projectId && !!tables && tables.length > 0,
  });

  const isLoading = tablesLoading || relsLoading || columnsLoading;

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-64">
        <Loader2 className="w-8 h-8 animate-spin text-purple-400" />
      </div>
    );
  }

  if (tablesError) {
    return (
      <div className="text-center py-16 text-red-400">
        Failed to load schema. Please try again.
      </div>
    );
  }

  if (!tables || tables.length === 0) {
    return (
      <div className="text-center py-16">
        <Table2 className="w-12 h-12 text-text-tertiary mx-auto mb-3" />
        <p className="text-text-secondary">No tables found</p>
        <p className="text-sm text-text-tertiary mt-1">
          Create tables using the SQL Editor to see them here
        </p>
      </div>
    );
  }

  return (
    <SchemaCanvas
      tables={tables}
      columns={columnsMap || {}}
      relationships={relationships || []}
      positions={[]}
    />
  );
}
