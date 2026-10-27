import { useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import Button, { IconButton } from "./Button.jsx";
import Icon from "./Icon.jsx";
import { Input } from "./Field.jsx";

const EXIT_MS = 160;
const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/**
 * A modal dialog: radius 14 on a blurred scrim, lifting in. Escape and a click
 * on the scrim close it; Tab stays inside; focus returns where it was.
 *
 * @param {object} props
 * @param {boolean} props.open
 * @param {() => void} props.onClose
 * @param {React.ReactNode} [props.title]
 * @param {string} [props.icon]     icon beside the title
 * @param {React.ReactNode} [props.footer] the button bar (right-aligned)
 * @param {React.ReactNode} [props.footerNote] muted text at the left of the bar
 * @param {number} [props.width]    max width in px (default 440)
 * @param {boolean} [props.dismissable] Escape / scrim close it (default true)
 * @param {boolean} [props.flush]   no body padding (lists, diffs)
 */
export function Dialog({ open, onClose, title, icon, footer, footerNote, width = 440, dismissable = true, flush = false, className = "", children }) {
  const [mounted, setMounted] = useState(open);
  const panel = useRef(null);
  const restore = useRef(null);
  const titleId = useId();
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const last = useRef({ title, icon, footer, footerNote, children });
  if (open) last.current = { title, icon, footer, footerNote, children };
  const shown = open ? { title, icon, footer, footerNote, children } : last.current;

  useEffect(() => {
    if (open) {
      setMounted(true);
      return undefined;
    }
    const timer = setTimeout(() => setMounted(false), EXIT_MS);
    return () => clearTimeout(timer);
  }, [open]);

  // Where focus returns: noted before the dialog's own fields take focus (they
  // do in effects, which run before this component's).
  useLayoutEffect(() => {
    if (!open) return;
    const active = document.activeElement;
    restore.current = active && !panel.current?.contains(active) ? active : null;
  }, [open]);

  useEffect(() => {
    if (!open) return undefined;
    const frame = requestAnimationFrame(() => {
      const node = panel.current;
      if (!node || node.contains(document.activeElement)) return;
      const preferred = node.querySelector("[autofocus], [data-autofocus]") || node.querySelector(".dlg-body " + FOCUSABLE.split(",")[3]) || node.querySelector(".btn-primary, .btn-danger-fill") || node.querySelector(FOCUSABLE);
      (preferred || node).focus({ preventScroll: true });
    });
    const onKey = (event) => {
      if (event.key === "Escape" && dismissable) {
        event.stopPropagation();
        closeRef.current?.();
      } else if (event.key === "Tab") {
        const items = [...(panel.current?.querySelectorAll(FOCUSABLE) || [])].filter((el) => el.offsetParent !== null);
        if (!items.length) return;
        const first = items[0];
        const lastItem = items[items.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          lastItem.focus();
        } else if (!event.shiftKey && document.activeElement === lastItem) {
          event.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("keydown", onKey, true);
      const back = restore.current;
      if (back && typeof back.focus === "function" && document.contains(back)) back.focus({ preventScroll: true });
    };
  }, [open, dismissable]);

  if (!(open || mounted) || typeof document === "undefined") return null;
  return createPortal(
    <div
      className={`dlg-scrim ${open ? "" : "leaving"}`.trim()}
      onMouseDown={(event) => {
        if (dismissable && event.target === event.currentTarget) closeRef.current?.();
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={shown.title ? titleId : undefined}
        tabIndex={-1}
        className={`dlg ${flush ? "dlg-flush" : ""} ${className}`.replace(/\s+/g, " ").trim()}
        style={{ "--dlg-w": `${width}px` }}
      >
        {shown.title != null && (
          <header className="dlg-head">
            {shown.icon && <Icon name={shown.icon} size={16} className="dlg-icon" />}
            <h2 id={titleId}>{shown.title}</h2>
            {dismissable && <IconButton icon="x" label="关闭" size="sm" className="dlg-x" onClick={() => closeRef.current?.()} />}
          </header>
        )}
        <div className="dlg-body">{shown.children}</div>
        {(shown.footer || shown.footerNote) && (
          <footer className="dlg-foot">
            {shown.footerNote && <span className="dlg-note">{shown.footerNote}</span>}
            <div className="dlg-actions">{shown.footer}</div>
          </footer>
        )}
      </div>
    </div>,
    document.body,
  );
}

// ------------------------------------------------------ imperative dialogs
let queue = [];
let seq = 0;
const listeners = new Set();
const emit = () => listeners.forEach((fn) => fn());
const push = (entry) =>
  new Promise((resolve) => {
    queue = [...queue, { ...entry, id: ++seq, resolve }];
    emit();
  });
const settle = (id, value) => {
  const entry = queue.find((e) => e.id === id);
  if (!entry || entry.closing) return;
  queue = queue.map((e) => (e.id === id ? { ...e, closing: true } : e));
  emit();
  entry.resolve(value);
  setTimeout(() => {
    queue = queue.filter((e) => e.id !== id);
    emit();
  }, EXIT_MS);
};

/**
 * Asks for a line of text. Resolves to the trimmed text, or null when cancelled.
 *
 * @param {object} options
 * @param {string} options.title
 * @param {React.ReactNode} [options.message]   explanation above the field
 * @param {string} [options.label]              field label
 * @param {string} [options.defaultValue]
 * @param {string} [options.placeholder]
 * @param {string} [options.confirmLabel]       default "确定"
 * @param {(value: string) => string|null|undefined} [options.validate] returns an error message to block
 * @param {boolean} [options.selectName]        select the file name without folder and extension
 * @param {boolean} [options.mono]              monospace field (paths)
 * @param {string} [options.icon]
 * @returns {Promise<string|null>}
 */
export function prompt(options) {
  return push({ kind: "prompt", ...options });
}

/**
 * Asks to confirm an action. Resolves true or false.
 *
 * @param {object} options
 * @param {string} options.title
 * @param {React.ReactNode} [options.message]
 * @param {string} [options.confirmLabel] default "确定"
 * @param {string} [options.cancelLabel]  default "取消"
 * @param {boolean} [options.danger]      a red confirm button (focus then starts on Cancel)
 * @returns {Promise<boolean>}
 */
export function confirm(options) {
  return push({ kind: "confirm", ...options });
}

/** Shows a message with one button. Resolves when dismissed. */
export function alert(options) {
  return push({ kind: "alert", ...options });
}

function PromptBody({ entry, onDone }) {
  const [value, setValue] = useState(entry.defaultValue ?? "");
  const [error, setError] = useState("");
  const input = useRef(null);

  useEffect(() => {
    const el = input.current;
    if (!el) return;
    el.focus();
    if (entry.selectName && el.value) {
      const start = el.value.lastIndexOf("/") + 1;
      const dot = el.value.lastIndexOf(".");
      el.setSelectionRange(start, dot > start ? dot : el.value.length);
    } else {
      el.select();
    }
  }, [entry.selectName]);

  const submit = (event) => {
    event?.preventDefault();
    const text = value.trim();
    const problem = entry.validate?.(text) ?? (text ? null : "不能为空");
    if (problem) {
      setError(problem);
      input.current?.focus();
      return;
    }
    onDone(text);
  };

  return (
    <form onSubmit={submit} className="dlg-form">
      {entry.message && <div className="dlg-message">{entry.message}</div>}
      {entry.label && <label className="field-label" htmlFor={`dlg-in-${entry.id}`}>{entry.label}</label>}
      <Input
        id={`dlg-in-${entry.id}`}
        ref={input}
        value={value}
        mono={entry.mono}
        placeholder={entry.placeholder}
        size="lg"
        onChange={(e) => (setValue(e.target.value), setError(""))}
        aria-invalid={!!error || undefined}
      />
      {error ? <div className="field-error">{error}</div> : entry.hint ? <div className="field-hint">{entry.hint}</div> : null}
      <button type="submit" hidden />
    </form>
  );
}

/** Renders prompt/confirm/alert dialogs. Mount once near the root. */
export function DialogHost() {
  const entries = useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => queue,
  );
  const entry = entries.find((e) => !e.closing) ?? entries[0];
  const forms = useRef({});
  if (!entry) return null;
  const open = !entry.closing;
  const cancelValue = entry.kind === "prompt" ? null : entry.kind === "confirm" ? false : undefined;
  const cancel = () => settle(entry.id, cancelValue);

  if (entry.kind === "prompt") {
    return (
      <Dialog
        key={entry.id}
        open={open}
        onClose={cancel}
        title={entry.title}
        icon={entry.icon}
        className="dlg-compact"
        width={entry.width ?? 420}
        footer={
          <>
            <Button onClick={cancel}>{entry.cancelLabel ?? "取消"}</Button>
            <Button variant="primary" onClick={() => forms.current[entry.id]?.()}>
              {entry.confirmLabel ?? "确定"}
            </Button>
          </>
        }
      >
        <PromptWithHandle entry={entry} register={(fn) => (forms.current[entry.id] = fn)} onDone={(text) => settle(entry.id, text)} />
      </Dialog>
    );
  }

  // A destructive question starts on Cancel, so an Enter pressed out of habit deletes nothing.
  const safeFirst = entry.kind === "confirm" && !!entry.danger;
  return (
    <Dialog
      key={entry.id}
      open={open}
      onClose={cancel}
      title={entry.title}
      icon={entry.icon ?? (entry.danger ? "alert" : undefined)}
      className="dlg-compact"
      width={entry.width ?? 400}
      footer={
        <>
          {entry.kind === "confirm" && (
            <Button data-autofocus={safeFirst || undefined} onClick={cancel}>
              {entry.cancelLabel ?? "取消"}
            </Button>
          )}
          <Button variant={entry.danger ? "danger-fill" : "primary"} data-autofocus={!safeFirst || undefined} onClick={() => settle(entry.id, entry.kind === "confirm" ? true : undefined)}>
            {entry.confirmLabel ?? (entry.kind === "alert" ? "好" : "确定")}
          </Button>
        </>
      }
    >
      {entry.message && <div className="dlg-message">{entry.message}</div>}
    </Dialog>
  );
}

/** Lets the footer's confirm button submit the prompt's form. */
function PromptWithHandle({ entry, register, onDone }) {
  const formRef = useRef(null);
  useEffect(() => {
    register(() => formRef.current?.querySelector('button[type="submit"]')?.click());
  });
  return (
    <div ref={formRef}>
      <PromptBody entry={entry} onDone={onDone} />
    </div>
  );
}

export default Dialog;
