import { Toaster } from 'sonner';

// Drawers (SidePanel) open from the right with their actions at the bottom, so
// toasts stack bottom-left where they never cover a drawer's footer.
export function AppToaster() {
  return <Toaster richColors position="bottom-left" />;
}
