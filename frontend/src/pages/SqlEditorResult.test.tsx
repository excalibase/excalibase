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
});
