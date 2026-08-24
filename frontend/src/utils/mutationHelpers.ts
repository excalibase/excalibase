import { toast } from 'sonner';

export function onMutationError(action: string) {
  return (err: Error) => {
    toast.error(`Failed to ${action}`, { description: err.message });
  };
}
