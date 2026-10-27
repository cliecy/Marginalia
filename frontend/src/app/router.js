import { useSyncExternalStore } from "react";

/**
 * Hash routing.
 *
 *   #/                       home
 *   #/<vault path>           a note in the notes workspace (backward compatible)
 *   #!/notes                 the notes workspace with nothing open
 *   #!/<view>[/<sub>]        any other view, e.g. #!/settings/ai, #!/chats
 *   #/graph                  the graph view
 *
 * A route is { view, sub, path, state }: `path` is set for notes, `sub` for a
 * view's own tab, `state` is the history entry's state (one-shot hints such as
 * { anchor, mode } when a note is opened).
 */

/** Every view the router knows; anything else falls back to home. */
export const VIEWS = ["home", "notes", "chats", "graph", "organize", "prompts", "sync", "api", "magpie", "settings"];

const decode = (text) => {
  try {
    return decodeURIComponent(text);
  } catch {
    return text;
  }
};

/** Encodes a vault path for the hash, keeping "/" readable but escaping "#", "?" and "%". */
export const encodePath = (path) => encodeURI(path).replace(/#/g, "%23").replace(/\?/g, "%3F");

/** Parses a location hash into a route (without state). */
export function parseHash(hash) {
  const raw = (hash || "").replace(/^#/, "");
  if (raw.startsWith("!/") || raw === "!") {
    const [view = "home", ...rest] = raw.slice(2).split("/").filter(Boolean).map(decode);
    if (!VIEWS.includes(view)) return { view: "home", sub: null, path: null };
    if (view === "notes") return { view: "notes", sub: null, path: rest.length ? rest.join("/") : null };
    return { view, sub: rest.length ? rest.join("/") : null, path: null };
  }
  const path = decode(raw.replace(/^\/+/, ""));
  if (!path) return { view: "home", sub: null, path: null };
  if (path === "graph") return { view: "graph", sub: null, path: null };
  return { view: "notes", sub: null, path };
}

/** Builds the hash for a route-like object ({view, sub?, path?}). */
export function hashFor({ view = "home", sub = null, path = null } = {}) {
  if (view === "notes" && path) return `#/${encodePath(path)}`;
  if (view === "home") return "#/";
  return `#!/${view}${sub ? `/${sub.split("/").map(encodeURIComponent).join("/")}` : ""}`;
}

// ------------------------------------------------------------------ store
const listeners = new Set();
let cache = { key: null, route: null };

function readState() {
  try {
    return history.state ?? null;
  } catch {
    return null;
  }
}

function snapshot() {
  const state = readState();
  const key = `${location.hash}\u0000${state ? JSON.stringify(state) : ""}`;
  if (cache.key !== key) cache = { key, route: { ...parseHash(location.hash), state } };
  return cache.route;
}

const notify = () => listeners.forEach((fn) => fn());

if (typeof window !== "undefined") {
  window.addEventListener("popstate", notify);
  window.addEventListener("hashchange", notify);
}

/** The current route (outside React). */
export const currentRoute = () => snapshot();

/**
 * Goes to a route or a hash.
 *
 * @param {string | {view: string, sub?: string|null, path?: string|null}} target  "#!/chats", or a route
 * @param {{replace?: boolean, state?: object|null}} [options] replace: no new history entry; state: one-shot hints
 */
export function navigate(target, { replace = false, state = null } = {}) {
  const hash = typeof target === "string" ? (target.startsWith("#") ? target : `#${target}`) : hashFor(target);
  const same = hash === location.hash || (hash === "#/" && (location.hash === "" || location.hash === "#"));
  if (same && !state && !replace) return;
  if (replace || same) history.replaceState(state, "", hash);
  else history.pushState(state, "", hash);
  notify();
}

/** React hook: the current route, re-rendering on every navigation. */
export function useRoute() {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    snapshot,
    snapshot,
  );
}
