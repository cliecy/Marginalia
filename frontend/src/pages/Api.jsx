import { useMemo, useState } from "react";
import "./Api.css";
import { useApp } from "../app/AppContext.jsx";
import { useAsync, useStoredState } from "../app/hooks.js";
import { api } from "../lib/api.js";
import { listMcpTools, openApiOperations } from "../lib/appApi.js";
import { openExternal } from "../lib/native.js";
import { TopbarActions } from "../shell/Topbar.jsx";
import { Button, CopyField, IconButton, Page, PageIntro, SearchInput, Segmented, Skeleton, StatusDot, copyText, toast } from "../ui/index.js";
import { rotateToken, useConnection } from "./api/connection.js";
import Operations from "./api/Operations.jsx";
import SectionFold from "./api/SectionFold.jsx";
import Snippets from "./api/Snippets.jsx";
import { mcpSnippets, openApiSnippets, restSnippets } from "./api/snippets.js";
import Tools from "./api/Tools.jsx";

/** The listener's state as a dot and a word. */
const STATES = {
  running: { tone: "green", label: "运行中" },
  down: { tone: "amber", label: "没有在运行" },
  off: { tone: "grey", label: "已关闭" },
  unknown: { tone: "grey", label: "状态未知" },
};

/** The faint note beside the 接入 heading, per host. */
const CONNECT_NOTES = {
  desktop: "只监听本机回环地址 · 令牌可以随时换新",
  server: "令牌只对登录后的网页可见 · 可以随时换新",
};

const MODE_HINTS = {
  rest: "标准的 JSON 接口：读写笔记、全文搜索、反链、图谱、Git 历史，以及 SSE 流式的 AI 对话。适合脚本和自己的小工具。",
  mcp: "Model Context Protocol。Claude Code、Codex、Cursor 等 AI 工具接入后，可以直接搜索、阅读、写入和整理你的笔记。",
  openapi: "全部公开接口的 OpenAPI 3.1 描述：导入 Postman、Insomnia、Bruno，或者交给 AI 生成一个客户端。",
};

/**
 * The status line at the top (Magpie's gateway card): a dot, what is
 * listening and how it is doing, and the address as a pill that copies itself.
 */
function StatusCard({ conn, counts, onEnable, enabling, onSettings }) {
  const state = STATES[conn.state] || STATES.unknown;
  const name = conn.kind === "server" ? "Marginalia 服务" : "本地 API";
  let sub;
  if (conn.state === "off") sub = "开启后，Claude Code、Codex 和你的脚本才能连接到知识库。";
  else if (conn.state === "down") sub = conn.error ? `已启用，但监听没有起来：${conn.error}` : "已启用，但监听没有起来：端口可能被占用了，去设置里换一个试试。";
  else sub = [conn.kind === "server" ? "marginalia serve" : "REST · MCP · OpenAPI，一个地址、一个令牌", counts].filter(Boolean).join(" · ");

  const copy = async () => {
    if (await copyText(conn.base)) toast.ok("已复制地址");
  };

  return (
    <div className="list api-gw">
      <div className="row">
        <StatusDot tone={state.tone} className="api-gw-dot" />
        <div className="who">
          <div className="name">
            {name}
            <span className={`api-gw-state api-gw-${conn.state}`}>{state.label}</span>
          </div>
          <div className="sub">{sub}</div>
        </div>
        {conn.state === "off" && conn.canConfigure ? (
          <Button variant="soft" size="sm" icon="zap" loading={enabling} onClick={onEnable}>
            开启
          </Button>
        ) : conn.state === "down" && conn.canConfigure ? (
          <Button size="sm" iconRight="chevron-right" onClick={onSettings}>
            换个端口
          </Button>
        ) : conn.base ? (
          <button type="button" className="api-url" title="复制地址" onClick={copy}>
            <code>{conn.base}</code>
          </button>
        ) : (
          <Skeleton width={150} height={24} radius={6} />
        )}
      </div>
    </div>
  );
}

/** The token as a masked value with copy, reveal and (where allowed) rotate. */
function TokenValue({ conn, onRotate, rotating }) {
  if (conn.tokenLoading) return <Skeleton width={220} height={26} radius={7} />;
  if (conn.tokenError && !conn.token) {
    return (
      <div className="api-token-err">
        <span>读不到令牌：{conn.tokenError.message}</span>
        <Button size="sm" variant="ghost" icon="refresh" onClick={conn.reload}>
          重试
        </Button>
      </div>
    );
  }
  return (
    <CopyField
      value={conn.token}
      secret
      actions={conn.canRotate && <IconButton icon="refresh" size="sm" label="换一个新令牌" disabled={rotating} onClick={onRotate} />}
    />
  );
}

/**
 * API 与 MCP — how other programs reach the knowledge base, after Magpie's
 * gateway page: the status line, a 接入 card (REST · MCP · OpenAPI, address,
 * token, ready-to-paste examples), then the REST endpoints from the OpenAPI
 * document and the live MCP tool list.
 */
export default function Api() {
  const ctx = useApp();
  const conn = useConnection();
  const [modePref, setMode] = useStoredState("mn.api.mode", "mcp");
  const mode = modePref;
  const [examples, setExamples] = useStoredState("mn.api.examples", {});
  const [folds, setFolds] = useStoredState("mn.api.folds", { connect: true, ops: true, tools: true });
  const fold = (key) => (open) => setFolds((f) => ({ ...f, [key]: open }));
  const [opQuery, setOpQuery] = useState("");
  const [toolQuery, setToolQuery] = useState("");
  const [rotating, setRotating] = useState(false);
  const [enabling, setEnabling] = useState(false);

  const openapi = useAsync(() => api.openapi(), []);
  const ops = useMemo(() => (openapi.data ? openApiOperations(openapi.data) : []), [openapi.data]);
  const tools = useAsync(() => (conn.token ? listMcpTools(conn.token) : Promise.resolve(null)), [conn.token]);
  const toolList = tools.data || [];
  const toolsLoading = tools.loading || conn.tokenLoading;
  const toolsError = tools.error || (!conn.tokenLoading && !conn.token ? conn.tokenError : null);

  const counts = [ops.length ? `${ops.length} 个接口` : "", toolList.length ? `${toolList.length} 个 MCP 工具` : ""].filter(Boolean).join(" · ");

  const tabs = useMemo(() => {
    if (mode === "rest") return restSnippets(conn);
    if (mode === "openapi") return openApiSnippets(conn);
    return mcpSnippets(conn);
  }, [mode, conn.base, conn.mcpUrl, conn.openapiUrl, conn.token, conn.command]); // eslint-disable-line react-hooks/exhaustive-deps

  const busy = openapi.loading || toolsLoading;
  const refresh = () => {
    conn.reload();
    openapi.reload();
    tools.reload();
  };

  const onRotate = async () => {
    setRotating(true);
    const token = await rotateToken(ctx);
    if (token) conn.setToken(token);
    setRotating(false);
  };

  const onEnable = async () => {
    setEnabling(true);
    try {
      await ctx.updateSettings({ api: { enabled: true } });
      await conn.reload();
      toast.ok("本地 API 已开启");
    } catch (err) {
      toast.err(`没能开启：${err.message}`);
    } finally {
      setEnabling(false);
    }
  };

  const modes = [
    { value: "rest", label: "REST" },
    { value: "mcp", label: "MCP" },
    { value: "openapi", label: "OpenAPI" },
  ];

  return (
    <Page className="api-page">
      <TopbarActions>
        <IconButton icon="refresh" label="刷新" className={busy ? "api-busy" : ""} onClick={refresh} />
      </TopbarActions>

      <StatusCard conn={conn} counts={counts} onEnable={onEnable} enabling={enabling} onSettings={() => ctx.go("settings", "api")} />

      <PageIntro className="api-intro">
        其他程序和本地 AI（Claude Code、Codex、Cursor……）通过这个地址读写你的知识库：REST 给脚本，MCP 给 AI 工具。令牌等于完整的读写权限，只交给你信任的程序。
        {conn.canConfigure && (
          <button type="button" className="api-intro-link" onClick={() => ctx.go("settings", "api")}>
            端口与令牌
          </button>
        )}
      </PageIntro>

      <SectionFold title="接入" open={folds.connect} onToggle={fold("connect")} controls="api-connect" note={CONNECT_NOTES[conn.kind]} />
      {folds.connect && (
        <div className="card api-connect" id="api-connect">
          <label>方式</label>
          <div>
            <Segmented size="sm" value={mode} onChange={setMode} options={modes} ariaLabel="接入方式" />
            <p className="api-hint">{MODE_HINTS[mode]}</p>
          </div>

          {mode === "rest" && (
            <>
              <label>Base URL</label>
              <div>
                <CopyField value={conn.base} />
                <p className="api-hint">接口都在 /api 下，返回 JSON。</p>
              </div>
            </>
          )}
          {mode === "mcp" && (
            <>
              <label>MCP URL</label>
              <div>
                <CopyField value={conn.mcpUrl} />
                <p className="api-hint">Streamable HTTP。不能设置请求头的客户端，可以在地址后面加上 ?token=&lt;令牌&gt;。</p>
              </div>
              <label>stdio</label>
              <div>
                <CopyField value={`${conn.command} mcp`} />
                <p className="api-hint">本机命令行方式，不需要令牌：应用开着时转发给它，没开时直接读写笔记库。</p>
              </div>
            </>
          )}
          {mode === "openapi" && (
            <>
              <label>文档</label>
              <div>
                <CopyField value={conn.openapiUrl} actions={<IconButton icon="external" size="sm" label="在浏览器中打开" onClick={() => openExternal(conn.openapiUrl)} />} />
                <p className="api-hint">文档本身是公开的，不含令牌；调用接口时再带上令牌。</p>
              </div>
            </>
          )}
          {mode !== "openapi" && (
            <>
              <label>令牌</label>
              <div>
                <TokenValue conn={conn} onRotate={onRotate} rotating={rotating} />
                <p className="api-hint">
                  请求头写 Authorization: Bearer &lt;令牌&gt;。REST 和 MCP 共用这一个令牌。
                </p>
              </div>
            </>
          )}

          <label>示例</label>
          <div>
            <Snippets tabs={tabs} value={examples[mode]} onChange={(id) => setExamples((e) => ({ ...e, [mode]: id }))} secret={conn.token} />
          </div>
        </div>
      )}

      {(
        <>
          <SectionFold
            title="接口"
            count={ops.length || null}
            open={folds.ops}
            onToggle={fold("ops")}
            controls="api-ops"
            note={openapi.data?.info?.version ? `OpenAPI ${openapi.data.openapi || "3.1"} · v${openapi.data.info.version}` : null}
          >
            <SearchInput size="sm" value={opQuery} onChange={setOpQuery} placeholder="查找接口" className="api-find" />
          </SectionFold>
          {folds.ops && (
            <div id="api-ops">
              <Operations ops={ops} loading={openapi.loading} error={openapi.error} onRetry={openapi.reload} query={opQuery} />
            </div>
          )}
        </>
      )}

      <SectionFold title="MCP 工具" count={toolList.length || null} open={folds.tools} onToggle={fold("tools")} controls="api-tools" note="实时读取自 tools/list">
        <SearchInput size="sm" value={toolQuery} onChange={setToolQuery} placeholder="查找工具" className="api-find" />
      </SectionFold>
      {folds.tools && (
        <div id="api-tools">
          <Tools tools={toolList} loading={toolsLoading} error={toolsError} onRetry={refresh} query={toolQuery} />
        </div>
      )}
    </Page>
  );
}
