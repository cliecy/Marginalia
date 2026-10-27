import { forwardRef, lazy, Suspense, useCallback, useImperativeHandle, useRef } from "react";

// CodeMirror and its Markdown/language packages are the largest part of the
// app; most visits only read. The editor loads on demand — and, so switching
// to 编辑 is instant, in the background once the app is idle.
let loading = null;
const load = () => (loading ??= import("./EditorCore.jsx"));
const EditorCore = lazy(load);
if (typeof window !== "undefined") {
  const idle = window.requestIdleCallback || ((fn) => setTimeout(fn, 1500));
  idle(() => load().catch(() => (loading = null)));
}

const METHODS = ["getSelection", "replaceSelection", "insertAtCursor", "replaceAll", "gotoLine"];

/**
 * The Markdown source editor (CodeMirror 6, see EditorCore.jsx), loaded lazily.
 *
 * Imperative handle (stable from the first render): `getSelection()`,
 * `replaceSelection(text)`, `insertAtCursor(text)`, `replaceAll(text)`,
 * `gotoLine(n)`. Calls made while CodeMirror is still loading are replayed
 * once it is ready; getSelection() answers "" until then.
 *
 * @param {object} props
 * @param {string} props.value
 * @param {string|number} [props.docKey]     changes when `value` is another document (resets undo)
 * @param {(text: string) => void} [props.onChange]
 * @param {() => void} [props.onSave]          ⌘S / Ctrl+S
 * @param {(text: string) => void} [props.onSelection]
 * @param {string} [props.assetsDir]           where pasted / dropped files are uploaded
 */
const Editor = forwardRef(function Editor(props, ref) {
  const inner = useRef(null);
  const queued = useRef([]);

  useImperativeHandle(
    ref,
    () =>
      Object.fromEntries(
        METHODS.map((name) => [
          name,
          (...args) => {
            if (inner.current) return inner.current[name](...args);
            if (name === "getSelection") return "";
            queued.current.push([name, args]);
            return undefined;
          },
        ]),
      ),
    [],
  );

  const attach = useCallback((handle) => {
    inner.current = handle;
    if (!handle) return;
    const calls = queued.current.splice(0);
    for (const [name, args] of calls) handle[name](...args);
  }, []);

  return (
    <Suspense fallback={<div className="editor editor-loading" aria-busy="true" aria-label="正在载入编辑器" />}>
      <EditorCore ref={attach} {...props} />
    </Suspense>
  );
});

export default Editor;
