/**
 * The HTTP client for the Marginalia API.
 *
 * Three hosts serve this page:
 *  - the desktop app's webview (`mygo://localhost`, same origin as the API),
 *  - a browser against `marginalia serve` (same origin),
 *  - `mygo dev`, where Vite serves the page at http://localhost:5173 and the API
 *    stays on the app's own scheme — then every URL must be absolute.
 * API_BASE covers the third case; it is "" otherwise.
 */
const inMyGo = typeof window !== "undefined" && !!window.mygo;

/** Prefix for every API/static URL ("" unless the page runs under `mygo dev`). */
export const API_BASE =
  inMyGo && /^https?:$/.test(location.protocol) ? (window.mygo?.platform === "win32" || /^Win/.test(navigator.platform) ? "http://mygo.localhost" : "mygo://localhost") : "";

const CREDENTIALS = API_BASE ? "include" : "same-origin";

/**
 * Sent with every request the app makes. A <form> cannot set a header, so the
 * server can tell the app's own calls from a form post that rendered note or
 * AI HTML might sneak in (it requires the header on unsafe, non-Bearer calls).
 */
export const CLIENT_HEADERS = Object.freeze({ "X-Marginalia-Client": "1" });

// keepalive requests (saves started while the page goes away) may carry at most 64 KiB in total.
const KEEPALIVE_MAX = 60000;

/** Makes a URL for `path` (which starts with "/") on the API's origin. */
export const apiUrl = (path) => `${API_BASE}${path}`;

/** An error answer from the API: `status` is the HTTP status, `code` the API's machine code ("no_vault"…). */
export class ApiError extends Error {
  constructor(status, message, code = null, data = null) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.data = data;
  }
}

async function failure(response, url) {
  let detail = response.statusText || `HTTP ${response.status}`;
  let code = null;
  let data = null;
  try {
    data = await response.json();
    if (typeof data.detail === "string") detail = data.detail;
    else if (data.detail !== undefined) detail = JSON.stringify(data.detail);
    code = data.code ?? null;
  } catch {}
  if (response.status === 401 && !url.includes("/auth/")) window.dispatchEvent(new Event("auth-required"));
  if (response.status === 409 && code === "no_vault") window.dispatchEvent(new Event("vault-required"));
  return new ApiError(response.status, detail, code, data);
}

async function request(method, url, body, headers = {}, { keepalive = false } = {}) {
  const options = { method, headers: { ...CLIENT_HEADERS, ...headers }, credentials: CREDENTIALS };
  if (body instanceof FormData) options.body = body;
  else if (body !== undefined) {
    options.headers["Content-Type"] = "application/json";
    options.body = JSON.stringify(body);
  }
  // Lets a save outlive the page (a reload or a closing window); the browser caps the body size.
  if (keepalive && (options.body?.length ?? 0) < KEEPALIVE_MAX) options.keepalive = true;
  let response;
  try {
    response = await fetch(apiUrl(url), options);
  } catch (err) {
    throw new ApiError(0, `无法连接到服务：${err.message || err}`, "network");
  }
  if (!response.ok) throw await failure(response, url);
  if (response.status === 202 || response.status === 204) return null;
  const text = await response.text();
  return text ? JSON.parse(text) : null;
}

const q = (params) => new URLSearchParams(params).toString();

export const api = {
  // auth and identity
  me: () => request("GET", "/api/auth/me"),
  login: (password) => request("POST", "/api/auth/login", { password }),
  logout: () => request("POST", "/api/auth/logout"),
  health: () => request("GET", "/healthz"),

  // vault
  tree: () => request("GET", "/api/tree"),
  note: (path) => request("GET", `/api/note?${q({ path })}`),
  /** options.keepalive: the save may outlive the page (sent while it unloads). */
  save: (path, content, base_etag, options) => request("PUT", "/api/note", { path, content, base_etag }, {}, options),
  create: (path, content = "", folder = false) => request("POST", "/api/note", { path, content, folder }),
  move: (path, to) => request("POST", "/api/move", { path, to }),
  remove: (path) => request("DELETE", `/api/note?${q({ path })}`),
  upload: (file, folder) => {
    const form = new FormData();
    form.append("file", file);
    if (folder) form.append("folder", folder);
    return request("POST", "/api/upload", form);
  },
  search: (text) => request("GET", `/api/search?${q({ q: text })}`),
  backlinks: (path) => request("GET", `/api/backlinks?${q({ path })}`),
  index: () => request("GET", "/api/index"),
  graph: (chats = false) => request("GET", `/api/graph?${q({ chats })}`),

  // git
  history: (path) => request("GET", `/api/history?${q({ path })}`),
  version: (path, rev) => request("GET", `/api/history/version?${q({ path, rev })}`),
  gitStatus: () => request("GET", "/api/git/status"),
  gitSync: () => request("POST", "/api/git/sync"),

  // AI and chats
  models: () => request("GET", "/api/ai/models"),
  prompts: () => request("GET", "/api/ai/prompts"),
  applyChangeset: (body) => request("POST", "/api/ai/apply", body),
  chats: () => request("GET", "/api/chats"),
  loadChat: (path) => request("GET", `/api/chats/load?${q({ path })}`),
  saveChat: (body) => request("PUT", "/api/chats", body),

  // external access
  openapi: () => request("GET", "/api/openapi.json"),
  /** Sends one JSON-RPC message to /mcp (token: the API token, when the host needs one). */
  mcp: (message, token) => request("POST", "/mcp", message, token ? { Authorization: `Bearer ${token}`, Accept: "application/json, text/event-stream" } : { Accept: "application/json, text/event-stream" }),

  // app settings and integrations
  appSettings: () => request("GET", "/api/app/settings"),
  updateAppSettings: (patch) => request("PUT", "/api/app/settings", patch),
  initVault: (path) => request("POST", "/api/app/vault/init", { path }),
  integrations: () => request("GET", "/api/app/integrations"),
  rotateToken: () => request("POST", "/api/app/token/rotate"),
  magpieUse: (model) => request("POST", "/api/app/magpie/use", model ? { model } : {}),
  magpieRegister: (transport = "stdio") => request("POST", "/api/app/magpie/register-mcp", { transport }),
  magpieUnregister: () => request("POST", "/api/app/magpie/unregister-mcp"),
};

/** URL of a vault file for <img>/<iframe>/downloads. */
export const fileUrl = (path) => apiUrl(`/api/file?${q({ path })}`);

/** POST and read a server-sent-event stream, calling onEvent for each JSON payload. */
export async function stream(url, body, onEvent, signal) {
  let response;
  try {
    response = await fetch(apiUrl(url), {
      method: "POST",
      headers: { ...CLIENT_HEADERS, "Content-Type": "application/json" },
      credentials: CREDENTIALS,
      body: JSON.stringify(body),
      signal,
    });
  } catch (err) {
    if (err.name === "AbortError") throw err;
    throw new ApiError(0, `无法连接到服务：${err.message || err}`, "network");
  }
  if (!response.ok) throw await failure(response, url);
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let boundary;
    while ((boundary = buffer.indexOf("\n\n")) >= 0) {
      const chunk = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      for (const line of chunk.split("\n")) {
        if (!line.startsWith("data:")) continue;
        let event;
        try {
          event = JSON.parse(line.slice(5));
        } catch {
          continue;
        }
        onEvent(event);
      }
    }
  }
}
