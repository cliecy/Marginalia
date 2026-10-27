import { isMac } from "../lib/native.js";

const MAC = { Mod: "⌘", Shift: "⇧", Alt: "⌥", Ctrl: "⌃", Enter: "↩", Escape: "esc", Backspace: "⌫", Up: "↑", Down: "↓", Left: "←", Right: "→" };
const PC = { Mod: "Ctrl", Shift: "Shift", Alt: "Alt", Ctrl: "Ctrl", Enter: "Enter", Escape: "Esc", Backspace: "Backspace", Up: "↑", Down: "↓", Left: "←", Right: "→" };

/** Formats a shortcut such as "Mod+Shift+K" for this platform: "⌘⇧K" on a Mac, "Ctrl+Shift+K" elsewhere. */
export function formatKeys(keys) {
  const map = isMac ? MAC : PC;
  const parts = keys.split("+").map((k) => map[k] ?? (k.length === 1 ? k.toUpperCase() : k));
  return isMac ? parts.join("") : parts.join("+");
}

/**
 * A keyboard shortcut hint.
 *
 * @param {object} props
 * @param {string} [props.keys] e.g. "Mod+K" — formatted for the platform
 * @param {React.ReactNode} [props.children] literal text instead of keys
 */
export default function Kbd({ keys, children, className = "" }) {
  return <kbd className={`kbd ${className}`.trim()}>{keys ? formatKeys(keys) : children}</kbd>;
}
