/**
 * App-level helpers for the settings, API, Magpie, onboarding and login pages —
 * the calls ./api.js does not make itself, and small pure helpers around them.
 *
 * Plain endpoint calls (settings, integrations, token rotation, Magpie, vault
 * init) live on `api` in ./api.js; this module adds:
 *  - listMcpTools(): a JSON-RPC `tools/list` through /mcp that never trips the
 *    app's "signed out" handling (a wrong MCP token is not a lost session),
 *  - openApiOperations(): the OpenAPI document flattened into a list,
 *  - path helpers for vault folders picked natively (macOS, Linux, Windows),
 *  - secret masking for display.
 */
import { API_BASE, ApiError, CLIENT_HEADERS, apiUrl } from "./api.js";

const CREDENTIALS = API_BASE ? "include" : "same-origin";

/**
 * Reads one JSON-RPC reply from a /mcp response body, which is plain JSON (the
 * Marginalia servers) or a server-sent event stream (Streamable HTTP servers
 * may answer that way).
 */
function readRpcReply(text, contentType, id) {
  const pick = (value) => (Array.isArray(value) ? value.find((m) => m?.id === id) ?? value[0] : value);
  if ((contentType || "").includes("text/event-stream")) {
    const payloads = text
      .split(/\r?\n/)
      .filter((line) => line.startsWith("data:"))
      .map((line) => {
        try {
          return JSON.parse(line.slice(5));
        } catch {
          return null;
        }
      })
      .filter(Boolean);
    return pick(payloads.find((m) => m.id === id) ?? payloads[payloads.length - 1] ?? null);
  }
  return pick(text ? JSON.parse(text) : null);
}

/**
 * Lists the MCP tools the server offers (`tools/list` through /mcp).
 *
 * @param {string} token the API token (sent as a Bearer header)
 * @param {{signal?: AbortSignal}} [options]
 * @returns {Promise<Array<{name: string, description?: string, inputSchema?: object, annotations?: {readOnlyHint?: boolean, destructiveHint?: boolean}}>>}
 */
export async function listMcpTools(token, { signal } = {}) {
  const id = 1;
  const headers = { ...CLIENT_HEADERS, "Content-Type": "application/json", Accept: "application/json, text/event-stream" };
  if (token) headers.Authorization = `Bearer ${token}`;
  let response;
  try {
    response = await fetch(apiUrl("/mcp"), {
      method: "POST",
      headers,
      credentials: CREDENTIALS,
      body: JSON.stringify({ jsonrpc: "2.0", id, method: "tools/list", params: {} }),
      signal,
    });
  } catch (err) {
    if (err.name === "AbortError") throw err;
    throw new ApiError(0, `无法连接到 MCP 服务：${err.message || err}`, "network");
  }
  if (response.status === 401) throw new ApiError(401, "令牌无效或缺失，MCP 服务拒绝了请求", "unauthorized");
  if (!response.ok) throw new ApiError(response.status, `MCP 服务返回了 HTTP ${response.status}`);
  let reply;
  try {
    reply = readRpcReply(await response.text(), response.headers.get("content-type"), id);
  } catch {
    throw new ApiError(response.status, "MCP 服务的回复不是有效的 JSON-RPC", "bad_reply");
  }
  if (!reply) throw new ApiError(response.status, "MCP 服务没有回复", "bad_reply");
  if (reply.error) throw new ApiError(500, reply.error.message || "MCP 调用失败", "rpc_error", reply.error);
  return Array.isArray(reply.result?.tools) ? reply.result.tools : [];
}

const METHODS = ["get", "post", "put", "patch", "delete"];

/**
 * Flattens an OpenAPI 3.x document into its operations, in document order.
 *
 * @param {object} spec
 * @returns {Array<{id: string, method: string, path: string, summary: string, description: string, tag: string,
 *   stream: boolean, params: Array<{name: string, in: string, required: boolean, description: string, type: string}>,
 *   body: Array<{name: string, required: boolean, description: string, type: string}>, bodyType: string}>}
 */
export function openApiOperations(spec) {
  const out = [];
  const paths = spec && typeof spec.paths === "object" ? spec.paths : {};
  for (const [path, item] of Object.entries(paths)) {
    if (!item || typeof item !== "object") continue;
    const shared = Array.isArray(item.parameters) ? item.parameters : [];
    for (const method of METHODS) {
      const op = item[method];
      if (!op) continue;
      const params = [...shared, ...(Array.isArray(op.parameters) ? op.parameters : [])]
        .map((p) => resolveRef(spec, p))
        .filter(Boolean)
        .map((p) => ({
          name: p.name,
          in: p.in,
          required: !!p.required,
          description: p.description || "",
          type: schemaType(spec, p.schema),
        }));
      const content = op.requestBody ? resolveRef(spec, op.requestBody)?.content || {} : {};
      const bodyType = Object.keys(content)[0] || "";
      const bodySchema = resolveRef(spec, content[bodyType]?.schema);
      const required = new Set(bodySchema?.required || []);
      const body = Object.entries(bodySchema?.properties || {}).map(([name, schema]) => ({
        name,
        required: required.has(name),
        description: resolveRef(spec, schema)?.description || "",
        type: schemaType(spec, schema),
      }));
      const responses = op.responses || {};
      const stream = Object.values(responses).some((r) => !!resolveRef(spec, r)?.content?.["text/event-stream"]);
      out.push({
        id: `${method} ${path}`,
        method: method.toUpperCase(),
        path,
        summary: op.summary || "",
        description: op.description || "",
        tag: Array.isArray(op.tags) && op.tags.length ? op.tags[0] : "",
        stream,
        params,
        body,
        bodyType,
      });
    }
  }
  return out;
}

/** Follows a local `$ref` ("#/components/…"); returns the value itself otherwise. */
function resolveRef(spec, value, depth = 0) {
  if (!value || typeof value !== "object" || typeof value.$ref !== "string" || depth > 8) return value;
  const parts = value.$ref.replace(/^#\//, "").split("/").map((p) => p.replace(/~1/g, "/").replace(/~0/g, "~"));
  let node = spec;
  for (const part of parts) node = node?.[part];
  return resolveRef(spec, node, depth + 1);
}

/** A short type label for a JSON schema: "string", "integer[]", "object"… */
function schemaType(spec, schema) {
  const s = resolveRef(spec, schema);
  if (!s || typeof s !== "object") return "";
  const type = Array.isArray(s.type) ? s.type.filter((t) => t !== "null").join(" | ") : s.type;
  if (type === "array") return `${schemaType(spec, s.items) || "any"}[]`;
  if (type) return type;
  if (s.enum) return "enum";
  if (s.oneOf || s.anyOf) return "any";
  return s.properties ? "object" : "";
}

/**
 * Masks a secret for display: its first characters, then dots. Copy the real
 * value, show this one.
 *
 * @param {string} secret
 * @param {number} [keep] characters shown in clear (default 4)
 */
export function maskSecret(secret, keep = 4) {
  if (!secret) return "";
  return `${secret.slice(0, Math.min(keep, Math.floor(secret.length / 4)))}${"•".repeat(12)}`;
}

/** Replaces every occurrence of `secret` in `text` with its masked form. */
export function maskIn(text, secret) {
  if (!secret || !text) return text || "";
  return String(text).split(secret).join(maskSecret(secret));
}

/** The separator a native path uses ("\\" for a Windows path, "/" otherwise). */
const separatorOf = (path) => (/^[a-zA-Z]:\\/.test(path) || (path.includes("\\") && !path.includes("/")) ? "\\" : "/");

/** Joins a folder and a name with the folder's own separator. */
export function joinPath(folder, name) {
  const sep = separatorOf(folder);
  return `${folder.replace(/[\\/]+$/, "")}${sep}${name.replace(/^[\\/]+/, "")}`;
}

/** The last segment of a native path ("/Users/me/Notes" → "Notes"). */
export function baseName(path) {
  return (path || "").replace(/[\\/]+$/, "").split(/[\\/]/).pop() || path || "";
}

/** A path for display: the home folder shortened to "~" (macOS / Linux). */
export function tildePath(path) {
  if (!path) return "";
  return path.replace(/^\/(?:Users|home)\/[^/]+(?=\/|$)/, "~");
}

/** True for an absolute native path (POSIX, "~/…", or a Windows drive / UNC path). */
export const isAbsolutePath = (path) => /^(\/|~\/|[a-zA-Z]:[\\/]|\\\\)/.test(path || "");

/**
 * Checks a vault folder name typed by the reader. Returns an error message,
 * or null when the name is fine.
 */
export function vaultNameProblem(name) {
  const text = (name || "").trim();
  if (!text) return "请输入名称";
  if (/[\\/:]/.test(text)) return "名称里不能有 / \\ 或 :";
  if (text.startsWith(".")) return "名称不能以 . 开头";
  if (text.length > 120) return "名称太长了";
  return null;
}

/**
 * Checks a CORS origin ("https://example.com", "http://localhost:5173").
 * Returns an error message, or null.
 */
export function originProblem(origin) {
  let url;
  try {
    url = new URL(origin);
  } catch {
    return `「${origin}」不是有效的地址`;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return `「${origin}」需要以 http:// 或 https:// 开头`;
  if (url.pathname !== "/" || url.search || url.hash || origin.endsWith("/")) return `「${origin}」只写协议、主机和端口，不要带路径`;
  return null;
}
