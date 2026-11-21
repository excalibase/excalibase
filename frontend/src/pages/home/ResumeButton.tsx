import { useResumeProject } from '../../hooks/useProvisioning';
import { serverErrorMessage } from '../../utils/serverError';

// Resume is the project's own flow: the same call the Settings page makes.
export function ResumeButton({ projectId, name, className }: { readonly projectId: string; readonly name: string; readonly className?: string }) {
  const resume = useResumeProject();
  return (
    <span className="inline-flex flex-col">
      <button type="button" className={className} disabled={resume.isPending} aria-label={`Resume ${name}`} onClick={() => resume.mutate(projectId)}>
        {resume.isPending ? 'Resuming…' : 'Resume'}
      </button>
      {resume.isPending && <output className="sr-only">Resuming {name}</output>}
      {resume.isError && <span role="alert" className="text-xs text-red-400">{serverErrorMessage(resume.error, 'Could not resume this project.')}</span>}
    </span>
  );
}
