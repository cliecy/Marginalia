/**
 * What the desktop shell offers the page, with quiet fallbacks in a browser.
 *
 * The generated MyGo client (./mygo.ts) is imported lazily and only inside the
 * app's webview, so the browser build never touches `window.mygo`. Events are
 * the exception (see listen() below): they are taken from the first moment.
 */
import { call, on } from "mygo-runtime";

/** True inside the Marginalia desktop app (a MyGo window). */
export const isDesktop = typeof window !== "undefined" && !!window.mygo;

/** True on macOS (for shortcut labels and the traffic-light inset). */
export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || "");

/** "darwin" | "win32" | "linux" | "web". */
export const platform = isDesktop ? window.mygo?.platform ?? "darwin" : "web";

let client = null;
const load = () => (client ??= import("./mygo.ts"));

/** Shows the native folder picker. Resolves to the absolute path, or "" when cancelled / in a browser. */
export async function pickFolder(title = "选择文件夹") {
  if (!isDesktop) return "";
  const { Native } = await load();
  return (await Native.pickFolder(title)) || "";
}

/** Reveals a vault-relative path ("" = the vault root) in Finder. No-op in a browser. */
export async function reveal(path = "") {
  if (!isDesktop) return;
  const { Native } = await load();
  await Native.reveal(path);
}

/** Reveals the app's data folder. No-op in a browser. */
export async function revealDataDir() {
  if (!isDesktop) return;
  const { Native } = await load();
  await Native.revealDataDir();
}

/** Opens an http(s)/mailto URL in the default browser (a new tab outside the app). */
export async function openExternal(url) {
  if (!isDesktop) {
    window.open(url, "_blank", "noopener,noreferrer");
    return;
  }
  const { Native } = await load();
  await Native.openExternal(url);
}

/**
 * Describes the running app: { version, os, arch, dataDir, apiURL }, or null in a browser.
 * Accepts either Go field names or json-tagged ones from the generated client.
 */
export async function appInfo() {
  if (!isDesktop) return null;
  const { Native } = await load();
  const info = (await Native.info()) || {};
  return {
    version: info.version ?? info.Version ?? "",
    os: info.os ?? info.OS ?? "",
    arch: info.arch ?? info.Arch ?? "",
    dataDir: info.dataDir ?? info.DataDir ?? "",
    apiURL: info.apiURL ?? info.apiUrl ?? info.APIURL ?? "",
  };
}

// ------------------------------------------------------------------ events
// MyGo holds events for a new window only until DOMContentLoaded and then drops
// those nobody listens to. The app subscribes much later (after /api/auth/me,
// login or onboarding), so a deep link that launched the app, or a menu command
// sent while the window was opening, would be lost. This module runs before
// DOMContentLoaded (main.jsx imports it statically), takes the events at once,
// and keeps them for the app's listeners: the latest deep link whenever it came,
// menu commands for a short while.
const MENU_KEEP_MS = 10000;
const listeners = new Map(); // name -> Set<listener>
const waiting = new Map(); // name -> [{payload, at}]

function deliver(name, payload) {
  const set = listeners.get(name);
  if (set?.size) {
    for (const fn of [...set]) {
      try {
        fn(payload);
      } catch (err) {
        console.error(err);
      }
    }
    return;
  }
  const entry = { payload, at: Date.now() };
  // Only the newest deep link matters; menu commands queue up.
  waiting.set(name, name === "open" ? [entry] : [...(waiting.get(name) || []), entry]);
}

function listen(name) {
  if (!isDesktop || listeners.has(name)) return;
  listeners.set(name, new Set());
  try {
    on(name, (payload) => deliver(name, payload));
  } catch {}
}

for (const name of ["menu", "open", "flush"]) listen(name);

function subscribe(name, listener) {
  if (!isDesktop) return () => {};
  listen(name);
  listeners.get(name).add(listener);
  // Hand over what came before anyone listened — after the subscriber's own mount has finished.
  const timer = waiting.get(name)?.length
    ? setTimeout(() => {
        const fresh = (waiting.get(name) || []).filter((e) => name !== "menu" || Date.now() - e.at < MENU_KEEP_MS);
        waiting.delete(name);
        for (const { payload } of fresh) listener(payload);
      }, 0)
    : null;
  return () => {
    clearTimeout(timer);
    listeners.get(name)?.delete(listener);
  };
}

/**
 * Listens to native menu commands ("new-note", "open-vault", "settings",
 * "toggle-sidebar", "reveal-vault", "search"). Returns an unsubscribe function.
 */
export const onMenu = (listener) => subscribe("menu", listener);

/** Listens to marginalia:// deep links ({path?, q?}). Returns an unsubscribe function. */
export const onOpenLink = (listener) => subscribe("open", listener);

/**
 * Lets the desktop shell have unsaved edits written before it closes or quits
 * the window: on its "flush" event, `flush()` runs (it should save and resolve
 * when done) and then the shell is told through `Native.Flushed`. Harmless
 * while the shell sends no such event. Returns an unsubscribe function.
 *
 * @param {() => Promise<unknown>} flush
 */
export const onFlushRequest = (flush) =>
  subscribe("flush", () => {
    Promise.resolve()
      .then(flush)
      .catch(() => {})
      .then(() => call("Native.Flushed"))
      .catch(() => {});
  });
