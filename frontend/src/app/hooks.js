import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { api } from "../lib/api.js";

const read = (key, fallback) => {
  try {
    const raw = localStorage.getItem(key);
    return raw === null ? fallback : JSON.parse(raw);
  } catch {
    return fallback;
  }
};

/**
 * State remembered in localStorage (JSON). Falls back to `initial` when storage
 * is unavailable or holds nothing.
 *
 * @template T
 * @param {string} key
 * @param {T} initial
 * @returns {[T, (value: T | ((prev: T) => T)) => void]}
 */
export function useStoredState(key, initial) {
  const [value, setValue] = useState(() => read(key, initial));
  useEffect(() => {
    try {
      localStorage.setItem(key, JSON.stringify(value));
    } catch {}
  }, [key, value]);
  return [value, setValue];
}

/** Whether a media query matches, live. */
export function useMediaQuery(query) {
  return useSyncExternalStore(
    (fn) => {
      const list = window.matchMedia(query);
      list.addEventListener("change", fn);
      return () => list.removeEventListener("change", fn);
    },
    () => window.matchMedia(query).matches,
    () => false,
  );
}

/** A stable function that always calls the latest `fn` (for listeners and timers). */
export function useEvent(fn) {
  const ref = useRef(fn);
  useLayoutEffect(() => {
    ref.current = fn;
  });
  return useCallback((...args) => ref.current?.(...args), []);
}

/** Adds a window event listener for the component's lifetime. */
export function useWindowEvent(name, handler) {
  const stable = useEvent(handler);
  useEffect(() => {
    window.addEventListener(name, stable);
    return () => window.removeEventListener(name, stable);
  }, [name, stable]);
}

/**
 * Runs an async loader and tracks its result.
 *
 * @template T
 * @param {() => Promise<T>} loader
 * @param {Array} deps re-run when these change
 * @returns {{data: T|undefined, error: Error|null, loading: boolean, reload: () => Promise<void>, setData: Function}}
 */
export function useAsync(loader, deps = []) {
  const [state, setState] = useState({ data: undefined, error: null, loading: true });
  const run = useEvent(loader);
  const seq = useRef(0);
  const reload = useCallback(async () => {
    const id = ++seq.current;
    setState((s) => ({ ...s, loading: true, error: null }));
    try {
      const data = await run();
      if (id === seq.current) setState({ data, error: null, loading: false });
    } catch (error) {
      if (id === seq.current) setState((s) => ({ ...s, error, loading: false }));
    }
  }, [run]);
  useEffect(() => {
    reload();
  }, deps); // eslint-disable-line react-hooks/exhaustive-deps
  const setData = useCallback((data) => setState((s) => ({ ...s, data: typeof data === "function" ? data(s.data) : data })), []);
  return { ...state, reload, setData };
}

// ----------------------------------------------------------------- models
let modelsCache = null;
let modelsPromise = null;
const modelListeners = new Set();

/** Loads (or reloads) the model list shared by every useModels() caller. */
export function reloadModels() {
  modelsPromise = api
    .models()
    .then((r) => ({ models: r.models || [], labels: r.labels || {}, default: r.default || "", error: r.error || null }))
    .catch((err) => ({ models: modelsCache?.models || [], labels: modelsCache?.labels || {}, default: modelsCache?.default || "", error: err.message }))
    .then((value) => {
      modelsCache = value;
      modelListeners.forEach((fn) => fn());
      return value;
    });
  return modelsPromise;
}

/**
 * The AI models the server offers, loaded once and shared:
 * { models: string[], labels: {id: name}, default: string, error: string|null, loading: boolean, reload() }.
 * `labels` carry Magpie's display names when the Go server provides them.
 */
export function useModels() {
  const value = useSyncExternalStore(
    (fn) => {
      modelListeners.add(fn);
      return () => modelListeners.delete(fn);
    },
    () => modelsCache,
  );
  useEffect(() => {
    if (!modelsCache && !modelsPromise) reloadModels();
  }, []);
  return {
    models: value?.models ?? [],
    labels: value?.labels ?? {},
    default: value?.default ?? "",
    error: value?.error ?? null,
    loading: !value,
    reload: reloadModels,
  };
}

/**
 * A tiny pub/sub for app-wide happenings. Events used by the shell:
 *   "vault:changed" {paths: string[]}  files changed outside the editor (sync, organize, uploads)
 *   "note:saved"    {path}             the open note was saved
 */
export function createEmitter() {
  const map = new Map();
  return {
    on(name, fn) {
      if (!map.has(name)) map.set(name, new Set());
      map.get(name).add(fn);
      return () => map.get(name)?.delete(fn);
    },
    emit(name, payload) {
      map.get(name)?.forEach((fn) => {
        try {
          fn(payload);
        } catch (err) {
          console.error(err);
        }
      });
    },
  };
}
