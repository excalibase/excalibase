import { useEffect, useRef } from 'react';
import { EditorState, Prec, type Extension } from '@codemirror/state';
import { EditorView, keymap, placeholder as placeholderText } from '@codemirror/view';
import { oneDark } from '@codemirror/theme-one-dark';
import { basicSetup, minimalSetup } from 'codemirror';

interface CodeEditorProps {
  readonly value: string;
  readonly onChange: (value: string) => void;
  readonly ariaLabel: string;
  readonly testId: string;
  readonly extensions?: readonly Extension[];
  readonly singleLine?: boolean;
  readonly onSubmit?: () => void;
  readonly placeholder?: string;
  readonly height?: string;
}

// A CodeMirror editor held as a controlled input. The extensions are read
// once; anything that changes later is read through a ref inside them.
export function CodeEditor({
  value,
  onChange,
  ariaLabel,
  testId,
  extensions = [],
  singleLine = false,
  onSubmit,
  placeholder,
  height,
}: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const onChangeRef = useRef(onChange);
  const onSubmitRef = useRef(onSubmit);
  onChangeRef.current = onChange;
  onSubmitRef.current = onSubmit;

  useEffect(() => {
    if (!hostRef.current) return;
    const submit = () => {
      onSubmitRef.current?.();
      return true;
    };
    const view = new EditorView({
      parent: hostRef.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          singleLine ? minimalSetup : basicSetup,
          oneDark,
          Prec.highest(keymap.of([{ key: singleLine ? 'Enter' : 'Mod-Enter', run: submit }])),
          EditorView.contentAttributes.of({ 'aria-label': ariaLabel }),
          EditorView.theme({
            '&': { fontSize: '13px', ...(height ? { height } : {}) },
            '.cm-scroller': { overflow: 'auto' },
          }),
          placeholder ? placeholderText(placeholder) : [],
          EditorView.updateListener.of((update) => {
            if (update.docChanged) onChangeRef.current(update.state.doc.toString());
          }),
          ...extensions,
        ],
      }),
    });
    viewRef.current = view;
    return () => {
      view.destroy();
      viewRef.current = null;
    };
    // The editor is built once; value changes are pushed in below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const view = viewRef.current;
    if (view && view.state.doc.toString() !== value) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } });
    }
  }, [value]);

  return <div ref={hostRef} data-testid={testId} className="rounded-md overflow-hidden border border-border-primary" />;
}
