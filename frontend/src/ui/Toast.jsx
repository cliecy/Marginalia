import { useSyncExternalStore } from "react";
import Icon from "./Icon.jsx";

let toasts = [];
let seq = 0;
const listeners = new Set();
const emit = () => listeners.forEach((fn) => fn());
const MAX = 3;

const ICONS = { ok: "check", err: "alert-circle", warn: "alert", info: null };

/** Removes a toast early. */
export function dismissToast(id) {
  if (!toasts.some((t) => t.id === id)) return;
  toasts = toasts.map((t) => (t.id === id ? { ...t, leaving: true } : t));
  emit();
  setTimeout(() => {
    toasts = toasts.filter((t) => t.id !== id);
    emit();
  }, 180);
}

/**
 * Shows a short message as a pill at the bottom centre of the window
 * (Magpie's lifted status), tinted by its tone.
 *
 * @param {React.ReactNode} message
 * @param {{tone?: "info"|"ok"|"err"|"warn", duration?: number}} [options] duration in ms (default 3200; errors 5200)
 * @returns {number} the toast's id (for dismissToast)
 */
export function toast(message, options = {}) {
  const tone = options.tone ?? "info";
  const id = ++seq;
  toasts = [...toasts.filter((t) => !t.leaving).slice(-(MAX - 1)), { id, message, tone }];
  emit();
  const duration = options.duration ?? (tone === "err" ? 5200 : 3200);
  if (duration > 0) setTimeout(() => dismissToast(id), duration);
  return id;
}

toast.ok = (message, options) => toast(message, { ...options, tone: "ok" });
toast.err = (message, options) => toast(message, { ...options, tone: "err" });
toast.warn = (message, options) => toast(message, { ...options, tone: "warn" });

/** Renders the toasts. Mount once near the root. */
export function ToastHost() {
  const items = useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => toasts,
  );
  if (!items.length) return null;
  return (
    <div className="toasts" role="status" aria-live="polite">
      {items.map((t) => (
        <div key={t.id} className={`toast toast-${t.tone} ${t.leaving ? "leaving" : ""}`.trim()} onClick={() => dismissToast(t.id)}>
          {ICONS[t.tone] && <Icon name={ICONS[t.tone]} size={13} />}
          <span>{t.message}</span>
        </div>
      ))}
    </div>
  );
}

export default toast;
