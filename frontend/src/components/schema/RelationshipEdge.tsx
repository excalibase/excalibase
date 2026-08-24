import { memo } from 'react';
import { getSmoothStepPath, type EdgeProps } from '@xyflow/react';

export const RelationshipEdge = memo(function RelationshipEdge({
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  selected,
}: EdgeProps) {
  const [edgePath] = getSmoothStepPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    borderRadius: 14,
  });

  return (
    <g>
      {/* Invisible hit area */}
      <path
        d={edgePath}
        fill="none"
        strokeWidth={20}
        stroke="transparent"
        className="cursor-pointer"
      />
      {/* Visible edge */}
      <path
        d={edgePath}
        fill="none"
        strokeWidth={selected ? 2.5 : 1.5}
        className={selected ? 'stroke-accent-primary' : 'stroke-text-tertiary'}
        markerEnd={`url(#marker-n-${selected ? 'selected' : 'default'})`}
        markerStart={`url(#marker-1-${selected ? 'selected' : 'default'})`}
      />
    </g>
  );
});
