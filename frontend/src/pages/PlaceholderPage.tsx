import { Construction } from 'lucide-react';

interface PlaceholderPageProps {
  readonly title: string;
  readonly description?: string;
}

export function PlaceholderPage({
  title,
  description = 'This feature is under development.',
}: PlaceholderPageProps) {
  return (
    <div className="text-center py-16">
      <Construction className="w-12 h-12 text-text-tertiary mx-auto mb-3" />
      <h3 className="text-lg font-semibold text-text-primary mb-1">{title}</h3>
      <p className="text-sm text-text-secondary">{description}</p>
    </div>
  );
}
