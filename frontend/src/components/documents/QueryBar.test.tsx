import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { EditorView } from '@codemirror/view';
import { QueryBar } from './QueryBar';
import { typeIntoEditor } from '../../test/codemirror';

describe('QueryBar', () => {
  test('Enter in a query field applies the query', async () => {
    const onApply = vi.fn();
    render(<QueryBar fields={['total']} onApply={onApply} />);
    const filter = screen.getByTestId('query-filter');
    typeIntoEditor(filter, '{total: 1}');
    const view = EditorView.findFromDOM(filter.querySelector('.cm-editor') as HTMLElement)!;
    view.focus();
    await userEvent.keyboard('{Enter}');
    expect(onApply).toHaveBeenCalledWith({ filter: '{"total":{"$numberInt":"1"}}' });
  });

  test('Reset clears the query', async () => {
    const onApply = vi.fn();
    render(<QueryBar fields={[]} onApply={onApply} />);
    typeIntoEditor(screen.getByTestId('query-projection'), '{a: 1}');
    await userEvent.click(screen.getByRole('button', { name: /reset/i }));
    expect(onApply).toHaveBeenCalledWith({});
    const view = EditorView.findFromDOM(screen.getByTestId('query-projection').querySelector('.cm-editor') as HTMLElement)!;
    expect(view.state.doc.toString()).toBe('');
  });
});
