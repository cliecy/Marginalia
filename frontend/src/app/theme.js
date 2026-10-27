/**
 * Appearance: "system" | "light" | "dark".
 *
 * The choice is applied as data-theme on <html> (none for "system"), which the
 * tokens in styles/tokens.css follow. The last choice is cached in
 * localStorage so the next start paints in it before any request returns; in
 * the desktop app the source of truth is the app settings (appearance.theme).
 */
const KEY = "mn.theme";
export const THEMES = ["system", "light", "dark"];

/** The cached theme choice ("system" when none). */
export function storedTheme() {
  try {
    const value = localStorage.getItem(KEY);
    return THEMES.includes(value) ? value : "system";
  } catch {
    return "system";
  }
}

const COLORS = { light: "#f4f4f6", dark: "#1a1a1e" };

/**
 * Applies a theme choice to the document and caches it.
 *
 * @param {"system"|"light"|"dark"} theme
 * @param {{animate?: boolean}} [options] crossfade the change (default false)
 */
export function applyTheme(theme, { animate = false } = {}) {
  const value = THEMES.includes(theme) ? theme : "system";
  const root = document.documentElement;
  const current = root.dataset.theme || "system";
  try {
    localStorage.setItem(KEY, value);
  } catch {}
  if (current === value) return;
  if (animate && !window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) {
    root.classList.add("theming");
    setTimeout(() => root.classList.remove("theming"), 360);
  }
  if (value === "system") delete root.dataset.theme;
  else root.dataset.theme = value;
  for (const meta of document.querySelectorAll('meta[name="theme-color"]')) {
    const media = meta.getAttribute("media") || "";
    const scheme = media.includes("dark") ? "dark" : "light";
    meta.setAttribute("content", value === "system" ? COLORS[scheme] : COLORS[value]);
  }
  window.dispatchEvent(new CustomEvent("mn-theme", { detail: value }));
}

/** The scheme actually showing: "light" or "dark". */
export function effectiveScheme() {
  const forced = document.documentElement.dataset.theme;
  if (forced === "light" || forced === "dark") return forced;
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}
