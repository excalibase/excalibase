import { act } from '@testing-library/react';
import { EditorView } from '@codemirror/view';

// jsdom has no layout, and CodeMirror measures text ranges after each update.
const emptyRects = () => Object.assign([], { item: () => null }) as unknown as DOMRectList;
const emptyRect = () => ({ x: 0, y: 0, top: 0, left: 0, bottom: 0, right: 0, width: 0, height: 0, toJSON: () => ({}) }) as DOMRect;
if (typeof Range !== 'undefined' && !Range.prototype.getClientRects) {
  Range.prototype.getClientRects = emptyRects;
  Range.prototype.getBoundingClientRect = emptyRect;
}

function viewIn(container: HTMLElement): EditorView {
  const editor = container.querySelector('.cm-editor');
  const view = editor ? EditorView.findFromDOM(editor as HTMLElement) : null;
  if (!view) throw new Error('no CodeMirror editor here');
  return view;
}

export function editorText(container: HTMLElement): string {
  return viewIn(container).state.doc.toString();
}

// Replaces the text of the CodeMirror editor rendered in container, the way a
// user typing into it would.
export function typeIntoEditor(container: HTMLElement, text: string) {
  const view = viewIn(container);
  act(() => {
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
  });
}
