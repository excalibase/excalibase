import { toast } from 'sonner';
import { serverErrorMessage } from './serverError';

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
        description: serverErrorMessage(err, 'The server did not say why.'),
      });
    },
  };
}

export { toast } from 'sonner';
