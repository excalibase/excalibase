import { toast } from 'sonner';

/**
 * Wrap a mutation's onSuccess/onError with toast notifications.
 * Usage: useMutation({ ...mutationToast('Table created', 'Failed to create table') })
 */
export function mutationToast(successMsg: string, errorMsg?: string) {
  return {
    onSuccess: () => {
      toast.success(successMsg);
    },
    onError: (err: Error) => {
      toast.error(errorMsg || 'Operation failed', {
        description: err.message,
      });
    },
  };
}

export { toast } from 'sonner';
