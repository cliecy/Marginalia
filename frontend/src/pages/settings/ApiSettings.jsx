import { useState } from "react";
import { useApp } from "../../app/AppContext.jsx";
import { Button, CopyField, Icon, IconButton, List, Skeleton } from "../../ui/index.js";
import { rotateToken, useConnection } from "../api/connection.js";
import { CommitInput, OriginsDialog, SettingToggle, intProblem } from "./controls.jsx";
import { SetHead, SetRow } from "./search.jsx";

/**
 * API — the loopback listener (on/off, port, CORS) in the desktop app, and the
 * token everywhere the server lets this page see it.
 */
export default function ApiSettings() {
  const ctx = useApp();
  const listener = ctx.settings?.api || {};
  const desktop = ctx.host.desktop;
  const conn = useConnection();
  const [originsOpen, setOriginsOpen] = useState(false);
  const [rotating, setRotating] = useState(false);
  const origins = listener.cors_origins || [];
  const url = ctx.integrations?.api?.url || listener.url || `http://${listener.host || "127.0.0.1"}:${listener.port || 8190}`;
  const running = ctx.integrations?.api?.running ?? listener.running;

  const save = async (patch) => {
    await ctx.updateSettings({ api: patch });
    await ctx.reloadIntegrations();
  };

  const onRotate = async () => {
    setRotating(true);
    const next = await rotateToken(ctx);
    if (next) conn.setToken(next);
    setRotating(false);
  };

  const failure = ctx.integrations?.api?.error || listener.error || "";
  const state = !listener.enabled ? (
    "关闭后，其他程序（包括 HTTP 方式的 MCP 客户端）无法连接"
  ) : running === false ? (
    <span className="set-test err" title={failure || undefined}>
      <Icon name="alert-circle" size={13} />
      <span>{failure ? `没有在运行：${failure}` : "已启用，但没有在运行：端口可能被占用了"}</span>
    </span>
  ) : (
    `运行中 · ${url}`
  );

  return (
    <>
      {desktop && (
        <List>
          <SetRow
            title="启用本地 API"
            sub={state}
            keywords="api 本地 启用 开启 监听 rest mcp listener"
            right={<SettingToggle checked={!!listener.enabled} onSave={(enabled) => save({ enabled })} label="启用本地 API" />}
          />
          <SetRow
            title="端口"
            sub={`只监听 ${listener.host || "127.0.0.1"}；端口被占用时会自动往后顺延`}
            keywords="端口 port 地址"
            right={
              <CommitInput
                value={listener.port ?? ""}
                width={88}
                inputMode="numeric"
                className="tabnum"
                disabled={!listener.enabled}
                validate={intProblem(1024, 65535, "端口")}
                onCommit={(value) => save({ port: Number(value) })}
                aria-label="端口"
              />
            }
          />
          <SetRow
            title="允许的 CORS 来源"
            sub={origins.length ? origins.join(" · ") : "未设置：浏览器里的网页不能跨域调用"}
            keywords="cors 跨域 来源 origin 浏览器"
            right={
              <Button size="sm" onClick={() => setOriginsOpen(true)}>
                编辑…
              </Button>
            }
          />
        </List>
      )}

      {desktop && <SetHead title="令牌" />}
      <List>
        <SetRow
          title="访问令牌"
          sub="REST 和 MCP 共用；换新后旧令牌立刻失效"
          keywords="令牌 token 密钥 bearer 轮换 rotate"
          right={
            conn.tokenLoading ? (
              <Skeleton width={200} height={26} radius={7} />
            ) : (
              <CopyField
                value={conn.token}
                secret
                className="set-token"
                actions={conn.canRotate && <IconButton icon="refresh" size="sm" label="换一个新令牌" disabled={rotating} onClick={onRotate} />}
              />
            )
          }
        />
        <SetRow
          icon="plug"
          title="API 与 MCP"
          sub="地址、示例代码，以及 Claude Code、Codex、Cursor 的接入方法"
          keywords="接入 示例 claude codex cursor mcp"
          chevron
          onClick={() => ctx.go("api")}
        />
      </List>

      <OriginsDialog open={originsOpen} origins={origins} onClose={() => setOriginsOpen(false)} onSave={(list) => save({ cors_origins: list })} />
    </>
  );
}
