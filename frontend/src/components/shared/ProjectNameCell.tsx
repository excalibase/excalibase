interface ProjectNameCellProps {
  readonly projectId: string;
  readonly projectName?: string;
}

// A project is known by the name its owner gave it; the generated id is shown
// second, for support and the API.
export function ProjectNameCell({ projectId, projectName }: ProjectNameCellProps) {
  const name = projectName?.trim();
  if (!name || name === projectId) {
    return <span className="font-medium text-text-primary">{projectId}</span>;
  }
  return (
    <span className="flex flex-col">
      <span className="font-medium text-text-primary">{name}</span>
      <span className="text-xs text-text-tertiary font-mono">{projectId}</span>
    </span>
  );
}
