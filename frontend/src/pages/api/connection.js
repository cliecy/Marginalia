import { useEffect, useRef, useState } from "react";
import { useApp } from "../../app/AppContext.jsx";
import { useEvent } from "../../app/hooks.js";
import { api } from "../../lib/api.js";
import { confirm, toast } from "../../ui/index.js";

const trimSlash = (url) => (url || "").replace(/\/+$/, "");

/**
 * How other programs reach this Marginalia, worked out for the host the page
 * runs in:
 *  - desktop app: the loopback API listener from the app settings / integrations,
 *  - `marginalia serve` in a browser: this page's own origin.
 *
 * The token comes from /api/app/integrations (app or session only).
 *
 * @returns {{
 *   kind: "desktop"|"server", state: "running"|"down"|"off"|"unknown", error: string,
 *   base: string, mcpUrl: string, openapiUrl: string, command: string,
 *   token: string, tokenLoading: boolean, tokenError: Error|null,
 *   canRotate: boolean, canConfigure: boolean, reload: () => Promise<void>, setToken: (t: string) => void,
 * }}
 */
export function useConnection() {
  const ctx = useApp();
  const { host, settings, integrations } = ctx;
  const info = integrations?.api || {};
  const [own, setOwn] = useState({ token: "", loading: true, error: null });
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);

  const reload = useEvent(async () => {
    setOwn((s) => ({ ...s, loading: true, error: null }));
    const token = (await ctx.reloadIntegrations())?.api?.token || "";
    if (!ctx.settings) await ctx.reloadSettings();
    if (live.current) setOwn({ token, loading: false, error: null });
  });

  useEffect(() => {
    reload();
  }, [reload]);

  const kind = host.desktop ? "desktop" : "server";

  let base = trimSlash(location.origin);
  let state = "running";
  if (kind === "desktop") {
    const listener = settings?.api;
    base = trimSlash(info.url || listener?.url || (listener ? `http://${listener.host}:${listener.port}` : ""));
    const enabled = listener ? listener.enabled !== false : true;
    const running = info.running ?? listener?.running;
    state = !enabled ? "off" : running === false ? "down" : running ? "running" : "unknown";
  }
  const mcpUrl = kind === "desktop" && info.mcp_url ? info.mcp_url : `${base}/mcp`;
  const openapiUrl = kind === "desktop" && info.openapi_url ? info.openapi_url : `${base}/api/openapi.json`;
  // The desktop app reports its own binary (api.command); "marginalia" is the
  // stopgap before integrations load, found on $PATH.
  const command = info.command || info.mcp_command || "marginalia";

  return {
    kind,
    state,
    // why the desktop listener is down ("listen tcp …: address already in use")
    error: kind === "desktop" ? info.error || settings?.api?.error || "" : "",
    base,
    mcpUrl,
    openapiUrl,
    command,
    token: info.token || own.token,
    tokenLoading: !info.token && own.loading,
    tokenError: info.token ? null : own.error,
    canRotate: true,
    canConfigure: kind === "desktop",
    reload,
    setToken: (token) => setOwn((s) => ({ ...s, token })),
  };
}

/**
 * Asks, then rotates the API token. Resolves to the new token, or null when
 * cancelled or failed (a toast says why).
 *
 * @param {{reloadIntegrations: () => Promise<unknown>}} ctx
 */
export async function rotateToken(ctx) {
  const ok = await confirm({
    title: "换一个新的访问令牌？",
    message: "旧令牌会立刻失效。用 HTTP 方式接入的程序（Claude Code、Cursor、脚本……）需要换上新令牌；通过 stdio 接入的不受影响。",
    confirmLabel: "换新令牌",
    danger: true,
  });
  if (!ok) return null;
  try {
    const { token } = await api.rotateToken();
    await ctx.reloadIntegrations();
    toast.ok("已换成新令牌");
    return token;
  } catch (err) {
    toast.err(`没能换新令牌：${err.message}`);
    return null;
  }
}
