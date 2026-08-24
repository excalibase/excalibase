export function MarkerDefinitions(): JSX.Element {
  return (
    <svg className="absolute w-0 h-0">
      <defs>
        {/* "1" marker - default */}
        <marker id="marker-1-default" viewBox="0 0 20 20" refX="10" refY="10" markerWidth="20" markerHeight="20">
          <circle cx="10" cy="10" r="6" className="fill-bg-secondary stroke-text-tertiary" strokeWidth="1.5" />
          <text x="10" y="14" textAnchor="middle" className="fill-text-tertiary" fontSize="10" fontWeight="600">1</text>
        </marker>
        {/* "N" marker - default */}
        <marker id="marker-n-default" viewBox="0 0 20 20" refX="10" refY="10" markerWidth="20" markerHeight="20">
          <circle cx="10" cy="10" r="6" className="fill-bg-secondary stroke-text-tertiary" strokeWidth="1.5" />
          <text x="10" y="14" textAnchor="middle" className="fill-text-tertiary" fontSize="10" fontWeight="600">N</text>
        </marker>
        {/* "1" marker - selected */}
        <marker id="marker-1-selected" viewBox="0 0 20 20" refX="10" refY="10" markerWidth="20" markerHeight="20">
          <circle cx="10" cy="10" r="6" className="fill-bg-secondary stroke-accent-primary" strokeWidth="1.5" />
          <text x="10" y="14" textAnchor="middle" className="fill-accent-primary" fontSize="10" fontWeight="600">1</text>
        </marker>
        {/* "N" marker - selected */}
        <marker id="marker-n-selected" viewBox="0 0 20 20" refX="10" refY="10" markerWidth="20" markerHeight="20">
          <circle cx="10" cy="10" r="6" className="fill-bg-secondary stroke-accent-primary" strokeWidth="1.5" />
          <text x="10" y="14" textAnchor="middle" className="fill-accent-primary" fontSize="10" fontWeight="600">N</text>
        </marker>
      </defs>
    </svg>
  );
}
