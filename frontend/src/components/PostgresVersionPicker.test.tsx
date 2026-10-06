import { describe, test, expect, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { PostgresVersionPicker } from './PostgresVersionPicker';
import type { PostgresCatalog } from '../api/postgresCatalog';

const CATALOG: PostgresCatalog = {
  majors: [
    { major: '14', available: true, documentDb: false, documentDbUnavailableReason: 'DocumentDB needs PostgreSQL 15 or later.' },
    { major: '16', available: true, documentDb: true },
    { major: '17', available: false, documentDb: false },
    { major: '15', available: true, documentDb: false },
  ],
};

type PickerProps = Parameters<typeof PostgresVersionPicker>[0];

function mount(props: Partial<PickerProps> = {}) {
  const onVersionChange = vi.fn();
  const onDocumentDbChange = vi.fn();
  render(
    <PostgresVersionPicker
      catalog={CATALOG}
      isLoading={false}
      error={null}
      version=""
      onVersionChange={onVersionChange}
      documentDb={false}
      onDocumentDbChange={onDocumentDbChange}
      {...props}
    />,
  );
  return { onVersionChange, onDocumentDbChange };
}

describe('PostgresVersionPicker', () => {
  test('nothing is pre-selected, and DocumentDB waits for a version', () => {
    mount();
    for (const tile of screen.getAllByRole('button')) expect(tile).toHaveAttribute('aria-pressed', 'false');
    expect(screen.getByTestId('documentdb-toggle')).toBeDisabled();
    expect(screen.getByTestId('documentdb-reason')).toHaveTextContent(/choose a postgresql version first/i);
  });

  test('picking a major reports it; an unpublished major cannot be picked', () => {
    const { onVersionChange } = mount();
    fireEvent.click(screen.getByTestId('pg-version-16'));
    expect(onVersionChange).toHaveBeenCalledWith('16');
    expect(screen.getByTestId('pg-version-17')).toBeDisabled();
    expect(screen.getByTestId('pg-version-17')).toHaveTextContent('Not published yet');
  });

  test('a major without DocumentDB says why, in the catalogue wording', () => {
    mount({ version: '14' });
    expect(screen.getByTestId('documentdb-toggle')).toBeDisabled();
    expect(screen.getByTestId('documentdb-reason')).toHaveTextContent('DocumentDB needs PostgreSQL 15 or later.');
  });

  test('a major without DocumentDB and no catalogue reason gets a default one', () => {
    mount({ version: '15' });
    expect(screen.getByTestId('documentdb-reason')).toHaveTextContent('DocumentDB is not available on PostgreSQL 15.');
  });

  test('a capable major lets DocumentDB be turned on', () => {
    const { onDocumentDbChange } = mount({ version: '16' });
    expect(screen.queryByTestId('documentdb-reason')).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId('documentdb-toggle'));
    expect(onDocumentDbChange).toHaveBeenCalledWith(true);
  });

  test('a version unknown to the catalogue leaves DocumentDB open', () => {
    mount({ version: '99' });
    expect(screen.getByTestId('documentdb-toggle')).toBeEnabled();
  });

  test('the DocumentDB engine lists only capable majors and includes it', () => {
    mount({ documentDbOnly: true });
    expect(screen.getByTestId('pg-version-16')).toBeInTheDocument();
    expect(screen.queryByTestId('pg-version-14')).not.toBeInTheDocument();
    expect(screen.getByTestId('documentdb-included')).toBeInTheDocument();
    expect(screen.queryByTestId('documentdb-toggle')).not.toBeInTheDocument();
  });

  test('while loading it says so', () => {
    mount({ isLoading: true, catalog: undefined });
    expect(screen.getByText(/loading supported versions/i)).toBeInTheDocument();
  });

  test('a failed catalogue offers no versions and says to retry', () => {
    mount({ error: new Error('down'), catalog: undefined });
    expect(screen.getByTestId('pg-version-error')).toHaveTextContent(/retry/i);
    expect(screen.queryByTestId('pg-version-selector')).not.toBeInTheDocument();
  });
});
