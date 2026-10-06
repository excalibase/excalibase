import { toast } from 'sonner';
import { serverErrorMessage } from './serverError';

export function onMutationError(action: string) {
  return (err: Error) => {
    toast.error(`Failed to ${action}`, { description: serverErrorMessage(err, 'The server did not say why.') });
  };
}
