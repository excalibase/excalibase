import { Link } from 'react-router-dom';
import { useDocumentTitle } from '../hooks/useDocumentTitle';

// Reached from the console navigation; the credits and usage widgets arrive with the Home page of this layout.
export function UsagePage() {
  useDocumentTitle('Usage & credits');
  return (
    <div className="mx-auto max-w-3xl space-y-3" data-testid="usage-page">
      <h1 className="text-xl font-semibold text-text-primary">Usage &amp; credits</h1>
      <p className="text-sm text-text-secondary">Credits left and usage by service will be shown here soon.</p>
      <Link to="/instances" className="text-sm text-purple-400 hover:underline">See your projects</Link>
    </div>
  );
}
