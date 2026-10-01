import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { SnapshotsPage } from './SnapshotsPage';
import { API_BASE } from '../api/base';

// EXC-531: the page shows what the server lists and downloads from the
// configured API, including a DocumentDB project's documents.
const snapshot = {
  id: 'p1-20261001-120000',
  projectId: 'p1',
  format: 'custom',
  size: 2048,
  createdAt: '2026-10-01T12:00:00Z',
  documents: true,
  schemaOnly: false,
  dataOnly: true,
};

vi.mock('../hooks/useRouteProjectId', () => ({ useRouteProjectId: () => 'p1' }));
vi.mock('../hooks/useSnapshots', () => ({
  useSnapshots: () => ({ data: [snapshot], isLoading: false }),
  useExportSnapshot: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteSnapshot: () => ({ mutate: vi.fn(), isPending: false }),
}));

describe('SnapshotsPage', () => {
  test('lists a snapshot by its id, size and contents, and downloads it from the API', () => {
    render(<SnapshotsPage />);
    expect(screen.getByText(snapshot.id)).toBeInTheDocument();
    expect(screen.getByText('2.0 KB')).toBeInTheDocument();
    expect(screen.getByRole('cell', { name: /Data only/ })).toBeInTheDocument();
    expect(screen.getByText(/documents/i, { selector: '[data-testid="snapshot-documents"]' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Download/i })).toHaveAttribute(
      'href',
      `${API_BASE}/provision/p1/snapshot/${snapshot.id}/download`,
    );
  });
});
