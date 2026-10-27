import { useMemo, useState } from "react";
import "./Magpie.css";
import { useApp } from "../app/AppContext.jsx";
import { timeAgo } from "../app/format.js";
import { reloadModels, useModels } from "../app/hooks.js";
import { api } from "../lib/api.js";
import { maskIn } from "../lib/appApi.js";
import { openExternal } from "../lib/native.js";
import { TopbarActions } from "../shell/Topbar.jsx";
import {
  Button,
  CopyButton,
  Dialog,
  EmptyState,
  Icon,
  IconButton,
  List,
  ListFoot,
  Page,
  PageIntro,
  Pill,
  Row,
  SearchInput,
  SectionHead,
  Segmented,
  Select,
  Skeleton,
  StatusDot,
  confirm,
  copyText,
  toast,
} from "../ui/index.js";
import { useConnection } from "./api/connection.js";
import { modelLabel, modelOptions } from "./settings/modelOptions.js";

const MAGPIE_REPO = "https://github.com/yetone/magpie";
const MODELS_FOLDED = 8;

/** "http://127.0.0.1:3425/v1" → "127.0.0.1:3425/v1". */
const shortUrl = (url) => (url || "").replace(/^https?:\/\//, "").replace(/\/$/, "");

/**
 * The last register / unregister run, kept for the session so the output is
 * still there after a trip to another page (not stored: an HTTP command
 * carries the token).
 */
let lastRun = null;

/** Magpie's bird on an ink tile, the way its own icon looks. */
function MagpieMark({ size = 44 }) {
  return (
    <span className="mp-mark" style={{ width: size, height: size }} aria-hidden="true">
      <Icon name="magpie" size={Math.round(size * 0.6)} stroke={1.5} />
    </span>
  );
}

/** The hero card: Magpie's mark, whether its gateway runs, and what to do about it. */
function Hero({ magpie, loading, checking, onCheck }) {
  const gateway = magpie?.gateway || {};
  const installUrl = magpie?.install_url || MAGPIE_REPO;
  const running = !!gateway.running;
  const installed = !!magpie?.cli;

  let tone = "grey";
  let state = "没有检测到";
  let line = "Magpie 是 yetone 做的本地模型网关：把 Codex、Copilot、Claude 等订阅汇成本机一个 OpenAI 兼容地址，再分给你所有的 AI 工具。";
  if (running) {
    tone = "green";
    state = "网关运行中";
    line = [gateway.version && `v${gateway.version}`, gateway.models != null && `${gateway.models} 个模型`, "各家模型账号汇成本机一个地址"].filter(Boolean).join(" · ");
  } else if (installed) {
    tone = "amber";
    state = "网关没有运行";
    line = "Magpie 已经装好了，打开它就会在本机启动网关。";
  }

  const copy = async () => {
    if (await copyText(gateway.url || "http://127.0.0.1:3425")) toast.ok("已复制网关地址");
  };

  return (
    <div className="card mp-hero">
      <MagpieMark />
      <div className="mp-hero-text">
        <div className="mp-title">
          <h2>Magpie</h2>
          {loading ? (
            <Skeleton width={72} height={16} radius={8} />
          ) : (
            <span className={`mp-state mp-state-${tone}`}>
              <StatusDot tone={tone} ring={tone !== "grey"} />
              {state}
            </span>
          )}
        </div>
        {loading ? <Skeleton width="62%" height={8} style={{ marginTop: 8 }} /> : <p>{line}</p>}
      </div>
      <div className="mp-hero-side">
        {loading ? null : running ? (
          <button type="button" className="mp-url" title="复制网关地址" onClick={copy}>
            <code>{shortUrl(gateway.url) || "127.0.0.1:3425"}</code>
          </button>
        ) : (
          <>
            {!installed && (
              <Button variant="primary" size="sm" icon="arrow-up-right" onClick={() => openExternal(installUrl)}>
                了解并安装
              </Button>
            )}
            <Button size="sm" icon="refresh" loading={checking} onClick={onCheck}>
              重新检测
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

/** The outcome of a `magpie library …` run: the command, its output, ok or not. */
function RunResult({ run, secret, onDismiss }) {
  return (
    <div className={`card mp-run ${run.ok ? "ok" : "err"}`} role="status">
      <div className="mp-run-head">
        <Icon name={run.ok ? "check-circle" : "alert-circle"} size={15} />
        <b>{run.title}</b>
        <span className="mp-run-time">{timeAgo(run.at)}</span>
        <IconButton icon="x" size="sm" label="收起" onClick={onDismiss} />
      </div>
      {run.command && (
        <div className="mp-run-cmd">
          <span aria-hidden="true">$</span>
          <code>{maskIn(run.command, secret)}</code>
          <CopyButton text={run.command} label="复制命令" />
        </div>
      )}
      <pre className="mp-run-out">{maskIn(run.output, secret) || "（没有输出）"}</pre>
    </div>
  );
}

/**
 * Asks before writing into every agent's config, with the transport choice
 * and the exact command that will run.
 */
function RegisterDialog({ open, onClose, onConfirm, busy, conn, apiUp }) {
  const [transport, setTransport] = useState("stdio");
  const command =
    transport === "stdio"
      ? `magpie library mcp add marginalia ${/\s/.test(conn.command) ? `"${conn.command}"` : conn.command} mcp`
      : `magpie library mcp add marginalia ${conn.mcpUrl}?token=${conn.token || "<令牌>"}`;
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="把知识库添加到 Magpie？"
      icon="magpie"
      width={520}
      footerNote="之后随时可以在这里移除。"
      footer={
        <>
          <Button onClick={onClose}>取消</Button>
          <Button variant="primary" icon="plus" loading={busy} onClick={() => onConfirm(transport)}>
            添加到 Magpie
          </Button>
        </>
      }
    >
      <div className="mp-dlg">
        <p className="dlg-message">
          Magpie 会把 <b>marginalia</b> 这个 MCP 服务器写进它管理的<b>每一个 Agent</b> 的配置（Claude Code、Codex、OpenCode……）。之后这些 Agent 都能搜索、阅读和修改你的笔记；每次修改照常记进 git，可以找回。
        </p>
        <div className="mp-dlg-row">
          <span className="mp-dlg-label">方式</span>
          <Segmented
            size="sm"
            value={transport}
            onChange={setTransport}
            options={[
              { value: "stdio", label: "stdio · 推荐" },
              { value: "http", label: "HTTP", disabled: !apiUp, title: apiUp ? undefined : "本地 API 没有在运行" },
            ]}
            ariaLabel="接入方式"
          />
        </div>
        <p className="mp-dlg-hint">
          {transport === "stdio"
            ? "Agent 直接启动本应用的 mcp 子命令：不需要令牌，应用没开也能用。"
            : "Agent 通过本地 API 连接：地址里带着访问令牌，应用需要开着；令牌换新后要重新添加。"}
        </p>
        <span className="mp-dlg-label">将要运行</span>
        <pre className="snip mp-dlg-cmd">
          <code>{maskIn(command, conn.token)}</code>
        </pre>
      </div>
    </Dialog>
  );
}

/** Magpie's models, searchable; a click makes one the default. */
function ModelList({ models, labels, current, onPick }) {
  const [query, setQuery] = useState("");
  const [all, setAll] = useState(false);
  const options = useMemo(() => modelOptions(models, labels), [models, labels]);
  const q = query.trim().toLowerCase();
  const filtered = q ? options.filter((o) => `${o.value} ${o.label}`.toLowerCase().includes(q)) : options;
  const shown = q || all ? filtered : filtered.slice(0, MODELS_FOLDED);

  const copyAll = async () => {
    if (await copyText(models.join("\n"))) toast.ok(`已复制 ${models.length} 个模型 ID`);
  };

  return (
    <>
      <SectionHead title="模型" note={`Magpie 提供 ${models.length} 个 · 点一下设为默认`}>
        <SearchInput size="sm" value={query} onChange={setQuery} placeholder="查找模型" className="mp-find" />
        <Button size="sm" variant="ghost" icon="copy" onClick={copyAll}>
          复制全部 ID
        </Button>
      </SectionHead>
      <List className="mp-models">
        {shown.length === 0 && <EmptyState compact icon="search" title="没有匹配的模型" />}
        {shown.map((o) => (
          <Row
            key={o.value}
            compact
            title={<code className="mp-id">{o.value}</code>}
            sub={o.hint ? o.label : null}
            meta={o.value === current ? <Pill tone="accent" icon="check">默认</Pill> : <span className="mp-pick">设为默认</span>}
            active={o.value === current}
            onClick={() => o.value !== current && onPick(o.value)}
          />
        ))}
        {!q && !all && filtered.length > shown.length && <ListFoot onClick={() => setAll(true)}>显示其余 {filtered.length - shown.length} 个</ListFoot>}
      </List>
    </>
  );
}

function MagpieIntegration() {
  const ctx = useApp();
  const conn = useConnection();
  const models = useModels();
  const magpie = ctx.integrations?.magpie || null;
  const gateway = magpie?.gateway || {};
  const running = !!gateway.running;
  const cli = magpie?.cli || "";
  const registered = magpie?.mcp_registered ?? null;
  const ai = ctx.settings?.ai || {};
  const using = (ai.provider || ctx.me.ai_provider) === "magpie";
  const defaultModel = ai.default_model || ctx.me.default_model || "";

  const [checking, setChecking] = useState(false);
  const [busy, setBusy] = useState("");
  const [dialog, setDialog] = useState(false);
  const [run, setRun] = useState(lastRun);
  const loading = !magpie && conn.tokenLoading;

  const remember = (value) => {
    lastRun = value;
    setRun(value);
  };

  const check = async () => {
    setChecking(true);
    await conn.reload();
    setChecking(false);
  };

  const switchToMagpie = async () => {
    setBusy("use");
    try {
      await api.magpieUse();
      await ctx.reloadSettings();
      ctx.reloadMe();
      const result = await reloadModels();
      toast.ok(result.error ? "已切换到 Magpie，但还读不到模型列表" : `已改用 Magpie · ${result.models.length} 个模型可用`);
    } catch (err) {
      toast.err(`没能切换到 Magpie：${err.message}`);
    } finally {
      setBusy("");
    }
  };

  const setDefault = async (model) => {
    try {
      await ctx.updateSettings({ ai: { default_model: model } });
      ctx.reloadMe();
      toast.ok(`默认模型：${modelLabel(model, models.labels)}`);
    } catch (err) {
      toast.err(`没能保存默认模型：${err.message}`);
    }
  };

  const register = async (transport) => {
    setBusy("register");
    try {
      const result = await api.magpieRegister(transport);
      remember({ ok: result.ok !== false, title: result.ok !== false ? "已添加到 Magpie" : "Magpie 没有接受这个配置", command: result.command, output: result.output, at: Date.now() });
      if (result.ok !== false) toast.ok("已添加到 Magpie 的资源库");
      setDialog(false);
    } catch (err) {
      remember({ ok: false, title: "添加失败", command: err.data?.command, output: err.data?.output || err.message, at: Date.now() });
      setDialog(false);
    } finally {
      setBusy("");
      ctx.reloadIntegrations();
    }
  };

  const unregister = async () => {
    const ok = await confirm({
      title: "从 Magpie 移除知识库？",
      message: "Magpie 会从每个 Agent 的配置里删掉 marginalia，之后它们就读不到你的笔记了。笔记本身不受影响。",
      confirmLabel: "移除",
      danger: true,
    });
    if (!ok) return;
    setBusy("unregister");
    try {
      const result = await api.magpieUnregister();
      remember({ ok: result.ok !== false, title: result.ok !== false ? "已从 Magpie 移除" : "移除没有成功", command: result.command, output: result.output, at: Date.now() });
      if (result.ok !== false) toast.ok("已从 Magpie 移除");
    } catch (err) {
      remember({ ok: false, title: "移除失败", command: err.data?.command, output: err.data?.output || err.message, at: Date.now() });
    } finally {
      setBusy("");
      ctx.reloadIntegrations();
    }
  };

  const needCli = cli ? undefined : "没有找到 magpie 命令行工具（它通常在 /Applications/magpie.app 里）";
  const apiUp = conn.state === "running";
  const options = modelOptions(models.models, models.labels, defaultModel);

  const sourceSub = using ? (
    <span className="mp-on">
      <StatusDot tone={running ? "green" : "amber"} ring={false} />
      {running ? (
        `正在使用 · 默认模型 ${modelLabel(defaultModel, models.labels) || "未选择"}`
      ) : (
        <>
          正在使用，但网关没有运行：AI 功能暂时用不了
          <button type="button" className="mp-link" onClick={() => ctx.go("settings", "ai")}>
            改用其他服务
          </button>
        </>
      )}
    </span>
  ) : running ? (
    `现在用的是 ${shortUrl(ai.base_url) || "未配置的模型来源"}；切换后 AI 对话和整理知识库都走 Magpie`
  ) : (
    "需要先打开 Magpie"
  );

  const mcpSub =
    registered === true ? (
      <span className="mp-on">
        <StatusDot tone="green" ring={false} />
        已接入 · Claude Code、Codex、OpenCode… 都能用 MCP 读写你的笔记
      </span>
    ) : (
      `通过 Magpie 的资源库，一次写进每个 Agent 的 MCP 配置${registered === null && cli ? "（读不到当前状态）" : ""}`
    );

  return (
    <Page className="mp-page">
      <TopbarActions>
        <IconButton icon="refresh" label="重新检测" className={checking ? "mp-busy" : ""} onClick={check} />
      </TopbarActions>

      <Hero magpie={magpie} loading={loading} checking={checking} onCheck={check} />

      <PageIntro className="mp-intro">
        两件事：让 Marginalia 用 Magpie 的模型；再把知识库作为 MCP 服务器交给 Magpie，由它分发给 Claude Code、Codex 等每一个 Agent。
      </PageIntro>

      <SectionHead title="集成" />
      <List className="mp-list">
        <Row
          icon={<span className="mp-ic"><Icon name="sparkles" size={16} /></span>}
          title="用 Magpie 作为 AI 模型来源"
          sub={sourceSub}
          right={
            using ? (
              <Select
                size="sm"
                width={210}
                menuWidth={340}
                searchable
                searchPlaceholder="搜索模型"
                prefix="模型"
                value={defaultModel}
                options={options}
                disabled={models.loading || !options.length}
                placeholder={models.loading ? "正在读取…" : "选择模型"}
                onChange={setDefault}
                ariaLabel="默认模型"
              />
            ) : (
              <Button size="sm" variant="soft" icon="magpie" disabled={!running} loading={busy === "use"} onClick={switchToMagpie} title={running ? undefined : "Magpie 网关没有运行"}>
                使用 Magpie
              </Button>
            )
          }
        />
        <Row
          icon={<span className="mp-ic"><Icon name="plug" size={16} /></span>}
          title="把知识库接入 Magpie 的所有 Agent"
          sub={needCli && registered !== true ? needCli : mcpSub}
          right={
            <>
              {(registered === true || (registered === null && cli)) && (
                <Button size="sm" variant="danger" disabled={!cli || !!busy} loading={busy === "unregister"} onClick={unregister} title={needCli}>
                  移除
                </Button>
              )}
              {registered !== true && (
                <Button size="sm" variant="soft" icon="plus" disabled={!cli || !!busy} loading={busy === "register"} onClick={() => setDialog(true)} title={needCli}>
                  添加到 Magpie
                </Button>
              )}
            </>
          }
        />
      </List>

      {run && <RunResult run={run} secret={conn.token} onDismiss={() => remember(null)} />}

      {using && running && models.models.length > 0 && <ModelList models={models.models} labels={models.labels} current={defaultModel} onPick={setDefault} />}

      <RegisterDialog open={dialog} onClose={() => setDialog(false)} onConfirm={register} busy={busy === "register"} conn={conn} apiUp={apiUp} />
    </Page>
  );
}

/**
 * Magpie — the integration page: is the gateway running, use it as the AI
 * provider (with a model picker that shows Magpie's names), and hand the
 * knowledge base to every agent Magpie manages through its MCP library.
 */
export default function Magpie() {
  return <MagpieIntegration />;
}
