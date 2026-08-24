import { useMemo } from 'react';
import {
  ReactFlow,
  Background,
  Controls,
  BackgroundVariant,
  type Node,
  type Edge,
  type NodeTypes,
  type EdgeTypes,
  useNodesState,
  useEdgesState,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { TableNode, type TableNodeData } from './TableNode';
import { RelationshipEdge } from './RelationshipEdge';
import { MarkerDefinitions } from './MarkerDefinitions';
import type { TableInfo, ColumnInfo, RelationshipInfo, TablePosition } from '../../types/schema';

interface SchemaCanvasProps {
  readonly tables: TableInfo[];
  readonly columns: Record<string, ColumnInfo[]>;
  readonly relationships: RelationshipInfo[];
  readonly positions: TablePosition[];
}

const nodeTypes: NodeTypes = { tableNode: TableNode };
const edgeTypes: EdgeTypes = { relationship: RelationshipEdge };

export function SchemaCanvas({ tables, columns, relationships, positions }: SchemaCanvasProps): JSX.Element {
  const fkColumnsByTable = useMemo(() => {
    const map: Record<string, Set<string>> = {};
    for (const rel of relationships) {
      if (!map[rel.sourceTable]) map[rel.sourceTable] = new Set();
      map[rel.sourceTable].add(rel.sourceColumn);
    }
    return map;
  }, [relationships]);

  const positionMap = useMemo(() => {
    const map: Record<string, { x: number; y: number }> = {};
    for (const p of positions) {
      map[p.tableName] = { x: p.positionX, y: p.positionY };
    }
    return map;
  }, [positions]);

  const initialNodes: Node[] = useMemo(() => {
    return tables.map((table, idx) => {
      const pos = positionMap[table.name] ?? {
        x: (idx % 4) * 300 + 50,
        y: Math.floor(idx / 4) * 350 + 50,
      };
      const data: TableNodeData = {
        label: table.name,
        columns: columns[table.name] ?? [],
        foreignKeyColumns: fkColumnsByTable[table.name] ?? new Set(),
      };
      return {
        id: table.name,
        type: 'tableNode',
        position: pos,
        data,
      };
    });
  }, [tables, columns, positions, fkColumnsByTable, positionMap]);

  const initialEdges: Edge[] = useMemo(() => {
    return relationships.map((rel) => ({
      id: rel.constraintName,
      source: rel.sourceTable,
      target: rel.targetTable,
      type: 'relationship',
    }));
  }, [relationships]);

  const [nodes, , onNodesChange] = useNodesState(initialNodes);
  const [edges, , onEdgesChange] = useEdgesState(initialEdges);

  return (
    <div className="w-full h-[calc(100vh-220px)] border border-border-primary rounded-xl overflow-hidden relative">
      <MarkerDefinitions />
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        fitView
        minZoom={0.1}
        maxZoom={5}
        snapToGrid
        snapGrid={[20, 20]}
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={16} size={1} />
        <Controls />
      </ReactFlow>
    </div>
  );
}
