import { describe, test, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ResultPanel } from './SqlEditorPage';

const columns = [{ name: 'g', dataType: 'INT4' }];

describe('SQL editor result', () => {
  test('says when the server cut the result short', () => {
    render(<ResultPanel result={{ columns, rows: [[1], [2]], truncated: true }} />);
    expect(screen.getByTestId('query-truncated')).toHaveTextContent(/first 2 rows/i);
  });

  test('says nothing extra for a complete result', () => {
    render(<ResultPanel result={{ columns, rows: [[1]] }} />);
    expect(screen.queryByTestId('query-truncated')).not.toBeInTheDocument();
  });

  // ADR 0041: a MySQL run says what the engine does not undo, on success and on failure.
  test("shows the server's notice beside a result and beside an error", () => {
    const notice = 'MySQL DDL is not transactional: statements before a failure stay applied.';
    const { unmount } = render(<ResultPanel result={{ columns, rows: [[1]], notice }} />);
    expect(screen.getByTestId('query-notice')).toHaveTextContent(notice);
    unmount();
    render(<ResultPanel result={{ error: 'Table exists', notice }} />);
    expect(screen.getByTestId('query-error')).toHaveTextContent('Table exists');
    expect(screen.getByTestId('query-notice')).toHaveTextContent(notice);
  });

  test('shows no notice when the server sends none', () => {
    render(<ResultPanel result={{ columns, rows: [[1]] }} />);
    expect(screen.queryByTestId('query-notice')).not.toBeInTheDocument();
  });
});
