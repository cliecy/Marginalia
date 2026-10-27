import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import "./Organize.css";
import { useApp } from "../app/AppContext.jsx";
import { useModels } from "../app/hooks.js";
import { noteTitle, parentFolder, timeAgo } from "../app/format.js";
import { api, stream } from "../lib/api.js";
import DiffView, { diffStats } from "../components/DiffView.jsx";
import { TopbarActions } from "../shell/Topbar.jsx";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  Icon,
  IconButton,
  List,
  Page,
  PageIntro,
  Pill,
  Popover,
  Row,
  SearchInput,
  SectionHead,
  Select,
  Spinner,
  TextArea,
  toast,
} from "../ui/index.js";

// ------------------------------------------------------------------ presets
/** The organize tasks on offer. `inbox` works on the inbox chats; `needsNote` on the last opened note. */
export const PRESETS = [
  {
    id: "inbox",
    label: "整理收件箱",
    icon: "inbox",
    hint: "把 Chats/ 中待整理的对话沉淀为知识笔记",
    task: "把下面这些原始对话中有长期价值的知识沉淀到知识库：提炼成原子化的主题笔记（已有相关笔记则合并更新），用 [[双链]] 连接相关概念，在笔记 frontmatter 的 sources 中引用来源对话，并更新知识地图。闲聊和一次性的问题不需要入库。",
    inbox: true,
  },
  {
    id: "note",
    label: "整理当前笔记",
    icon: "file-text",
    hint: "补全 frontmatter、结构化、加双链、拆分过长的主题",
    task: "整理当前打开的笔记：按知识库约定补全 frontmatter，优化结构；如果它混合了多个独立主题，拆分为多篇原子笔记并互相链接；为提到的、库中已有的概念加上 [[双链]]。",
    needsNote: true,
  },
  {
    id: "health",
    label: "健康检查",
    icon: "activity",
    hint: "更新知识地图、补全缺失的双链与标签、发现重复主题",
    task: "对整个知识库做一次体检：更新知识地图 index.md 使其覆盖所有主题笔记；为缺少 summary/tags 的笔记补全 frontmatter；在相关笔记之间补上缺失的 [[双链]]；指出并合并明显重复的主题。",
  },
  {
    id: "migrate",
    label: "初始化结构",
    icon: "layers",
    hint: "首次使用：按 AGENTS.md 的目录结构把现有笔记归位",
    task: "按照知识库约定（AGENTS.md）中的目录结构，把现有笔记移动到合适的文件夹，为每篇笔记补全 frontmatter，并生成知识地图 index.md。不要改写笔记正文内容。",
  },
  { id: "custom", label: "自定义", icon: "edit", hint: "用自然语言描述你想让 AI 怎么改知识库", task: "" },
];

/** Other names a request may use for a preset. */
const ALIASES = { init: "migrate", structure: "migrate", setup: "migrate", map: "migrate", check: "health", distill: "inbox", chat: "inbox", chats: "inbox", current: "note" };
const presetById = (id) => PRESETS.find((p) => p.id === (ALIASES[id] || id)) || null;

const OP_LABEL = { create: "新建", update: "修改", move: "移动", delete: "删除" };

// ------------------------------------------------------------------ store
// The run lives outside the page so it keeps going (and stays reviewable)
// while the reader looks at a note: agent runs take minutes.
const blank = () => ({
  phase: "setup", // setup | running | review | applying | done
  presetId: null,
  autoPreset: false, // the preset was picked for the reader, not by them
  task: "",
  chats: null, // null: every inbox chat; an array: exactly these
  model: "",
  // the run
  run: null, // {presetId, task, chats, model, path}
  steps: [],
  summary: "",
  count: 0,
  proposals: [],
  selected: new Set(),
  error: "",
  stopped: false,
  startedAt: 0,
  endedAt: 0,
  vaultKey: "",
  // applying
  applyError: "",
  applied: [],
  touched: [],
});

let state = blank();
const listeners = new Set();
const subscribe = (fn) => {
  listeners.add(fn);
  return () => listeners.delete(fn);
};
const snapshot = () => state;
function update(patch) {
  state = { ...state, ...(typeof patch === "function" ? patch(state) : patch) };
  listeners.forEach((fn) => fn());
}

let controller = null;
let mountedPages = 0;
let stepSeq = 0;

/** Marks the step in progress as finished (or failed / stopped). */
const settle = (steps, tone = "ok") => steps.map((step) => (step.tone === "wait" ? { ...step, tone } : step));
const step = (text, tone = "wait", note) => ({ id: ++stepSeq, text, tone, note });

/** Stops the run in progress without reporting it (a new request replaces it). */
function cancelRun() {
  const running = controller;
  controller = null;
  running?.abort();
}

/**
 * Runs the organize agent (POST /api/ai/agent, SSE) and folds its events into
 * the store: status → a timeline step, plan → the plan step, proposal → a
 * reviewable change (selected by default), done → the final summary,
 * error → the failure.
 */
async function runAgent(run, vaultKey) {
  cancelRun();
  const own = new AbortController();
  controller = own;
  update({
    phase: "running",
    run,
    steps: [step("正在连接模型…")],
    summary: "",
    count: 0,
    proposals: [],
    selected: new Set(),
    error: "",
    stopped: false,
    startedAt: Date.now(),
    endedAt: 0,
    vaultKey,
    applyError: "",
    applied: [],
    touched: [],
  });
  const found = [];
  let failure = "";
  try {
    await stream(
      "/api/ai/agent",
      { model: run.model, task: run.task, chats: run.chats, path: run.path },
      (event) => {
        if (controller !== own) return;
        if (event.event === "status") {
          update((s) => ({ steps: [...settle(s.steps), step(event.message)] }));
        } else if (event.event === "plan") {
          update((s) => ({
            summary: event.summary || "",
            count: event.count || 0,
            steps: [...settle(s.steps), { ...step(event.summary || "（没有说明）", "why"), note: `计划 ${event.count || 0} 项操作` }],
          }));
        } else if (event.event === "proposal") {
          found.push(event.proposal);
          const index = found.length - 1;
          update((s) => ({ proposals: [...found], selected: new Set([...s.selected, index]), steps: settle(s.steps) }));
        } else if (event.event === "done") {
          if (event.summary) update({ summary: event.summary });
        } else if (event.event === "error") {
          throw new Error(event.message || "整理失败");
        }
      },
      own.signal,
    );
  } catch (err) {
    if (err.name !== "AbortError") failure = err.message || String(err);
  }
  if (controller !== own) return; // replaced by a newer run or request
  controller = null;
  const stopped = own.signal.aborted;
  update((s) => {
    const tail = failure
      ? step(failure, "bad")
      : stopped
        ? step("已停止", "aside")
        : step(found.length ? `生成了 ${found.length} 项修改，等你确认` : "看过了，这次不需要修改", "ok");
    return { phase: "review", steps: [...settle(s.steps, failure ? "bad" : stopped ? "aside" : "ok"), tail], error: failure, stopped, endedAt: Date.now() };
  });
  if (!mountedPages && !stopped) {
    if (failure) toast.err(`整理失败：${failure}`);
    else toast.ok(found.length ? `整理方案已生成：${found.length} 项修改，回「整理知识库」确认` : "整理完成：AI 认为不需要修改");
  }
}

/** Writes the selected proposals (POST /api/ai/apply) and tells the app what changed. */
async function applySelected(ctx) {
  const s = state;
  const chosen = s.proposals.filter((_, i) => s.selected.has(i));
  if (!chosen.length || s.phase !== "review") return;
  update({ phase: "applying", applyError: "" });
  try {
    await ctx.flushEdits();
    const operations = chosen.map(({ before, ...op }) => op); // eslint-disable-line no-unused-vars
    const result = await api.applyChangeset({ summary: s.summary, operations, chats: s.run?.chats || [] });
    const touched = result?.touched || [];
    update({ phase: "done", applied: chosen, touched });
    toast.ok(`已写入 ${touched.length} 个文件，并提交到 git`);
    ctx.notifyVaultChanged(touched);
  } catch (err) {
    update({ phase: "review", applyError: err.message || String(err) });
  }
}

// ------------------------------------------------------------------ helpers
const readPref = (key) => {
  try {
    return localStorage.getItem(key) || "";
  } catch {
    return "";
  }
};
const writePref = (key, value) => {
  try {
    localStorage.setItem(key, value);
  } catch {}
};
const when = (value) => timeAgo(String(value || "").replace(" ", "T")) || String(value || "").slice(0, 16);
const elapsed = (ms) => {
  const seconds = Math.max(0, Math.round(ms / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
};

/** Where a proposal's text sits: folder in muted type after the name, as Magpie lists paths. */
function PathName({ path }) {
  const folder = parentFolder(path);
  return (
    <span className="org-path">
      <b>{noteTitle(path) || path}</b>
      {folder && <span className="org-path-dir">{folder}</span>}
    </span>
  );
}

// ------------------------------------------------------------------ pieces
/**
 * Which chats the agent reads: a pill for "every inbox chat", or chips for an
 * explicit choice, and a popover with search and checkboxes to change it.
 */
function ChatPicker({ chats, value, inboxDefault, onChange }) {
  const button = useRef(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const all = chats || [];
  const inbox = all.filter((c) => c.status === "inbox").map((c) => c.path);
  const selected = value ?? inbox;
  const chosen = new Set(selected);
  const byPath = new Map(all.map((c) => [c.path, c]));
  const q = query.trim().toLowerCase();
  const shown = all.filter((c) => !q || `${c.title} ${c.path}`.toLowerCase().includes(q));
  const groups = [
    { title: "待整理", items: shown.filter((c) => c.status === "inbox") },
    { title: "已整理", items: shown.filter((c) => c.status !== "inbox") },
  ].filter((g) => g.items.length);

  const toggle = (path) => {
    const next = new Set(chosen);
    if (next.has(path)) next.delete(path);
    else next.add(path);
    onChange([...next]);
  };

  useEffect(() => {
    if (open) setQuery("");
  }, [open]);

  return (
    <div className="org-chats">
      {value === null && inboxDefault && inbox.length > 0 ? (
        <Pill tone="accent" icon="inbox">
          全部待整理对话 · {inbox.length}
        </Pill>
      ) : (
        selected.map((path) => (
          <span key={path} className="org-chat" title={path}>
            <Icon name="message" size={12} />
            <span>{byPath.get(path)?.title || noteTitle(path)}</span>
            <button type="button" aria-label={`移除 ${byPath.get(path)?.title || noteTitle(path)}`} onClick={() => toggle(path)}>
              <Icon name="x" size={10} stroke={1.8} />
            </button>
          </span>
        ))
      )}
      {!selected.length && <span className="org-chats-none">{inboxDefault ? "没有待整理的对话" : "不带对话"}</span>}
      <button ref={button} type="button" className={`org-chats-add ${open ? "open" : ""}`.trim()} onClick={() => setOpen(!open)} aria-haspopup="dialog" aria-expanded={open}>
        <Icon name={selected.length ? "edit" : "plus"} size={12} />
        {selected.length ? "调整" : "选择对话"}
      </button>
      <Popover open={open} anchor={button.current} onClose={() => setOpen(false)} placement="bottom-start" width={340} className="org-pick" ariaLabel="选择对话">
        <div className="org-pick-search">
          <SearchInput size="sm" value={query} onChange={setQuery} placeholder="搜索对话" autoFocus />
        </div>
        <div className="org-pick-tools">
          <span className="tabnum">已选 {selected.length} 个</span>
          <button type="button" onClick={() => onChange(inbox)} disabled={!inbox.length}>
            全选待整理
          </button>
          <button type="button" onClick={() => onChange([])} disabled={!selected.length}>
            清空
          </button>
        </div>
        <div className="org-pick-list">
          {!chats ? (
            <div className="org-pick-none">
              <Spinner size={12} /> 加载中…
            </div>
          ) : !groups.length ? (
            <div className="org-pick-none">{all.length ? "没有匹配的对话" : "还没有对话。和 AI 聊过之后会出现在这里。"}</div>
          ) : (
            groups.map((group) => (
              <div key={group.title} className="org-pick-group">
                <div className="org-pick-head">
                  {group.title} <span className="tabnum">{group.items.length}</span>
                </div>
                {group.items.map((c) => (
                  <label key={c.path} className={`org-pick-row ${chosen.has(c.path) ? "on" : ""}`.trim()} title={c.path}>
                    <input type="checkbox" checked={chosen.has(c.path)} onChange={() => toggle(c.path)} />
                    <span className="org-pick-words">
                      <span className="org-pick-title">{c.title}</span>
                      <span className="org-pick-sub">
                        {when(c.updated)}
                        {c.messages ? ` · ${c.messages} 条消息` : ""}
                      </span>
                    </span>
                  </label>
                ))}
              </div>
            ))
          )}
        </div>
      </Popover>
    </div>
  );
}

/** One proposed change: include it or not, what and why, and its diff on demand. */
function ProposalRow({ proposal, on, open, locked, onToggle, onExpand, onOpenNote }) {
  const p = proposal;
  const hasDiff = p.op === "create" || p.op === "update" || p.op === "delete";
  const after = p.op === "delete" ? "" : p.after || "";
  const stats = useMemo(() => (hasDiff ? diffStats(p.before || "", after) : null), [hasDiff, p.before, after]);
  const onKeyDown = (event) => {
    if (event.target !== event.currentTarget || !hasDiff) return;
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onExpand();
    }
  };
  return (
    <div className={`org-prop ${on ? "" : "off"} ${open ? "open" : ""}`.replace(/\s+/g, " ").trim()}>
      <div
        className={`org-prop-head ${hasDiff ? "can-open" : ""}`.trim()}
        role={hasDiff ? "button" : undefined}
        tabIndex={hasDiff ? 0 : undefined}
        aria-expanded={hasDiff ? open : undefined}
        onClick={hasDiff ? onExpand : undefined}
        onKeyDown={onKeyDown}
      >
        <input type="checkbox" checked={on} disabled={locked} onChange={onToggle} onClick={(e) => e.stopPropagation()} aria-label={`应用：${OP_LABEL[p.op] || p.op} ${p.path}`} />
        <span className={`org-op op-${p.op}`}>{OP_LABEL[p.op] || p.op}</span>
        <div className="org-prop-words">
          <div className="org-prop-line">
            <PathName path={p.path} />
            {p.to && (
              <>
                <Icon name="arrow-right" size={12} className="org-prop-arrow" />
                <PathName path={p.to} />
              </>
            )}
          </div>
          {p.reason && <div className="org-prop-reason">{p.reason}</div>}
        </div>
        {stats && (
          <span className="org-prop-stat tabnum">
            {stats.added > 0 && <em className="add">+{stats.added}</em>}
            {stats.removed > 0 && <em className="del">−{stats.removed}</em>}
          </span>
        )}
        {p.op !== "create" && onOpenNote && (
          <IconButton
            icon="arrow-up-right"
            size="sm"
            label="打开现在的版本"
            className="org-prop-open"
            onClick={(event) => {
              event.stopPropagation();
              onOpenNote(p.path);
            }}
          />
        )}
        {hasDiff && <Icon name="chevron-right" size={14} className="org-prop-chev" />}
      </div>
      {open && hasDiff && (
        <div className="org-prop-diff">
          <DiffView before={p.before || ""} after={after} maxHeight="min(60vh, 520px)" />
        </div>
      )}
    </div>
  );
}

// ------------------------------------------------------------------ page
/**
 * 整理知识库 — the organize agent as a page. Pick a task (inbox, the current
 * note, a health check, setting up the structure, or your own words), the
 * chats and the model; the agent plans and drafts changes while a timeline
 * shows its progress; then review each proposed change with its diff, keep
 * the ones you want, and apply them (written and committed to git at once).
 *
 * Props: { route, sub, request, onRequestConsumed } — `request` comes from
 * ctx.startOrganize({preset, chats, task}).
 */
export default function Organize({ request, onRequestConsumed }) {
  const ctx = useApp();
  const s = useSyncExternalStore(subscribe, snapshot);
  const catalog = useModels();
  const [expanded, setExpanded] = useState(() => new Set());
  const [, setTick] = useState(0);
  const vaultKey = ctx.me?.vault?.path || ctx.me?.vault?.name || "";
  const noteReady = !!ctx.lastNote && /\.md$/i.test(ctx.lastNote) && (!ctx.treeLoaded || ctx.files.includes(ctx.lastNote));

  useEffect(() => {
    mountedPages += 1;
    return () => {
      mountedPages -= 1;
    };
  }, []);

  /**
   * Switches to a preset: its task, and its chats (every inbox chat for
   * 整理收件箱). `auto` marks a default the page picked by itself.
   */
  const choosePreset = (id, { task, chats, auto = false } = {}) => {
    const preset = presetById(id) || PRESETS[0];
    update({
      presetId: preset.id,
      autoPreset: auto,
      task: task ?? preset.task,
      chats: chats?.length ? chats : preset.inbox ? null : [],
    });
  };

  // First visit, or the vault changed under a finished run: start fresh.
  useEffect(() => {
    if (state.vaultKey && state.vaultKey !== vaultKey && state.phase !== "running") update(blank());
    if (!state.presetId) choosePreset(ctx.inbox ? "inbox" : "health", { auto: true });
    if (!ctx.chats) ctx.reloadInbox();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // The inbox count can arrive after a direct visit: when the page picked the
  // preset itself, prefer 整理收件箱 as soon as there is something in it.
  useEffect(() => {
    if (state.autoPreset && state.phase === "setup" && ctx.inbox > 0 && state.presetId !== "inbox") choosePreset("inbox", { auto: true });
  }, [ctx.inbox]); // eslint-disable-line react-hooks/exhaustive-deps

  // A request from another page (沉淀 from a chat, a quick action on 首页).
  useEffect(() => {
    if (!request) return;
    if (state.phase === "running") cancelRun();
    update({ ...blank(), model: state.model, vaultKey });
    choosePreset(request.preset || "inbox", { task: request.task ?? undefined, chats: request.chats });
    setExpanded(new Set());
    onRequestConsumed?.();
  }, [request]); // eslint-disable-line react-hooks/exhaustive-deps

  // The elapsed time ticks while the agent works.
  useEffect(() => {
    if (s.phase !== "running") return undefined;
    const timer = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(timer);
  }, [s.phase]);

  // ------------------------------------------------------------ the form
  const preset = presetById(s.presetId) || PRESETS[0];
  const inboxChats = useMemo(() => (ctx.chats || []).filter((c) => c.status === "inbox").map((c) => c.path), [ctx.chats]);
  const runChats = s.chats ?? (preset.inbox ? inboxChats : []);

  const storedModel = s.model || readPref("mn.model") || ctx.me?.default_model || "";
  const model = catalog.models.length && !catalog.models.includes(storedModel) ? catalog.default || catalog.models[0] : storedModel;
  const modelOptions = useMemo(() => {
    const list = catalog.models.length ? catalog.models : model ? [model] : [];
    const groups = [];
    const options = list.map((id) => {
      const group = id.includes("/") ? id.split("/")[0] : undefined;
      if (group && !groups.includes(group)) groups.push(group);
      const label = catalog.labels[id];
      return { value: id, label: label || id, hint: label && label !== id ? id : undefined, group };
    });
    return options.sort((a, b) => groups.indexOf(a.group) - groups.indexOf(b.group));
  }, [catalog.models, catalog.labels, model]);
  const modelLabel = (id) => catalog.labels[id] || id;

  const blocker = !s.task.trim()
    ? "先写下要 AI 做什么"
    : preset.inbox && !runChats.length
      ? "收件箱是空的：没有待整理的对话"
      : preset.needsNote && !noteReady
        ? "先在笔记里打开一篇笔记"
        : !model
          ? "还没有可用的模型"
          : "";
  const aiMissing = ctx.me?.ai_configured === false;

  const start = () => {
    if (blocker || aiMissing) return;
    setExpanded(new Set());
    runAgent(
      {
        presetId: preset.id,
        task: s.task.trim(),
        chats: runChats,
        model,
        path: noteReady ? ctx.lastNote : null,
      },
      vaultKey,
    );
  };
  const rerun = () => s.run && (setExpanded(new Set()), runAgent(s.run, vaultKey));
  const stop = () => controller?.abort();
  const backToSetup = () => update({ phase: "setup", applyError: "" });
  const discard = () => {
    update({ phase: "setup", proposals: [], selected: new Set(), steps: [], run: null, error: "", applyError: "" });
    setExpanded(new Set());
  };
  const reset = () => {
    update({ ...blank(), model: s.model, vaultKey });
    choosePreset(ctx.inbox ? "inbox" : "health", { auto: true });
    setExpanded(new Set());
  };

  const toggle = (index) =>
    update((st) => {
      const next = new Set(st.selected);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return { selected: next };
    });
  const allOn = s.proposals.length > 0 && s.selected.size === s.proposals.length;
  const toggleAll = () => update({ selected: allOn ? new Set() : new Set(s.proposals.map((_, i) => i)) });
  const expand = (index) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });

  // ------------------------------------------------------------ views
  const running = s.phase === "running";
  const applying = s.phase === "applying";
  const reviewing = s.phase === "review" || applying;
  const runPreset = presetById(s.run?.presetId) || preset;

  const topbar = (
    <TopbarActions>
      {s.phase === "setup" && (
        <Button variant="soft" icon="sparkles" onClick={start} disabled={!!blocker || aiMissing} title={blocker || undefined}>
          生成方案
        </Button>
      )}
      {running && (
        <>
          <Pill tone="accent" className="org-live">
            <Spinner size={11} />
            <span className="tabnum">{elapsed(Date.now() - s.startedAt)}</span>
          </Pill>
          <Button size="sm" variant="ghost" icon="x" onClick={stop}>
            停止
          </Button>
        </>
      )}
      {reviewing && s.proposals.length > 0 && (
        <Button variant="soft" icon="check" onClick={() => applySelected(ctx)} disabled={!s.selected.size || applying}>
          应用 {s.selected.size} 项
        </Button>
      )}
      {s.phase === "done" && (
        <Button variant="soft" icon="sparkles" onClick={reset}>
          新任务
        </Button>
      )}
    </TopbarActions>
  );

  const setupView = (
    <>
      {s.run && s.proposals.length > 0 && (
        <div className="org-notice accent" role="status">
          <Icon name="history" size={14} />
          <span className="grow">上一次的方案还在：{s.proposals.length} 项修改还没有应用。</span>
          <Button size="sm" onClick={() => update({ phase: "review" })}>
            回到方案
          </Button>
        </div>
      )}
      <SectionHead title="任务" />
      <div className="org-presets" role="radiogroup" aria-label="整理任务">
        {PRESETS.map((p) => {
          const disabled = p.needsNote && !noteReady;
          const on = preset.id === p.id;
          return (
            <button
              key={p.id}
              type="button"
              role="radio"
              aria-checked={on}
              className={`org-preset ${on ? "on" : ""}`.trim()}
              disabled={disabled}
              title={disabled ? "先在笔记里打开一篇笔记" : undefined}
              onClick={() => !on && choosePreset(p.id)}
            >
              <span className="org-preset-top">
                <span className="org-preset-ic">
                  <Icon name={p.icon} size={15} />
                </span>
                <b>{p.label}</b>
                {p.inbox && ctx.inbox > 0 && <Badge tone="accent">{ctx.inbox}</Badge>}
              </span>
              <span className="org-preset-hint">{p.needsNote && noteReady ? `${noteTitle(ctx.lastNote)}：${p.hint}` : p.hint}</span>
            </button>
          );
        })}
      </div>

      <SectionHead title="说明" note="可以修改，AI 会照着做" />
      <Card padded={false} className="org-form">
        <div className="org-form-body">
          <Field label="要做什么" inline>
            {(id) => (
              <TextArea
                id={id}
                rows={5}
                value={s.task}
                onChange={(e) => update({ task: e.target.value, autoPreset: false })}
                placeholder="例如：把所有关于强化学习的内容整理成一个专题，包含 PPO、DQN 的对比…"
                className="org-task"
              />
            )}
          </Field>
          <Field label="对话" inline hint={preset.inbox ? "这些对话里有长期价值的内容会被整理成笔记，之后标记为已整理。" : "可选：让 AI 参考这些对话。"}>
            <ChatPicker chats={ctx.chats} value={s.chats ?? (preset.inbox ? null : [])} inboxDefault={preset.inbox} onChange={(chats) => update({ chats, autoPreset: false })} />
          </Field>
          <Field label="模型" inline hint="整理会多次调用模型，挑一个擅长长文本的。">
            {(id) => (
              <div className="org-model" id={id}>
                <Select
                  value={model}
                  onChange={(next) => {
                    update({ model: next });
                    writePref("mn.model", next);
                  }}
                  options={modelOptions}
                  searchable
                  placeholder={catalog.loading ? "加载模型…" : "选择模型"}
                  searchPlaceholder="搜索模型"
                  menuWidth={320}
                  prefix={<Icon name="cpu" size={12} />}
                  ariaLabel="模型"
                />
                {catalog.error && (
                  <span className="org-model-warn" title={catalog.error}>
                    <Icon name="alert" size={12} /> 模型列表读取失败
                  </span>
                )}
              </div>
            )}
          </Field>
          {noteReady && (
            <Field label="参考笔记" inline hint="AI 读知识库时会优先看它。">
              <span className="org-ref" title={ctx.lastNote}>
                <Icon name="file-text" size={13} />
                <PathName path={ctx.lastNote} />
              </span>
            </Field>
          )}
        </div>
        <div className="org-form-foot">
          <span className="org-foot-note">
            <Icon name="lock" size={12} />
            {blocker && !aiMissing ? blocker : "AI 只会提出修改方案，你确认后才会写入，并自动记录到 git 历史。"}
          </span>
          <Button variant="primary" icon="sparkles" onClick={start} disabled={!!blocker || aiMissing}>
            生成修改方案
          </Button>
        </div>
      </Card>
    </>
  );

  const headline = running
    ? s.count
      ? `正在撰写修改（${s.proposals.length} / ${s.count}）`
      : "正在分析知识库…"
    : s.error
      ? "这次没能完成"
      : s.stopped
        ? "已停止"
        : s.proposals.length
          ? `${s.proposals.length} 项修改等你确认`
          : "不需要修改";
  const headTone = running ? "accent" : s.error ? "red" : s.stopped ? "grey" : "green";
  const headIcon = s.error ? "alert-circle" : s.stopped ? "minus" : "check";

  const runView = (
    <>
      <Card padded={false} className="org-run">
        <div className="org-run-head">
          <span className={`org-run-ic tone-${headTone}`}>{running ? <Spinner size={15} /> : <Icon name={headIcon} size={16} stroke={2} />}</span>
          <div className="org-run-words">
            <b>{headline}</b>
            <span>
              {runPreset.label} · {modelLabel(s.run?.model || model)}
              {s.run?.chats?.length ? ` · ${s.run.chats.length} 个对话` : ""} · <span className="tabnum">{elapsed((s.endedAt || Date.now()) - s.startedAt)}</span>
            </span>
          </div>
          <div className="org-run-actions">
            {running ? (
              <Button size="sm" icon="x" onClick={stop}>
                停止
              </Button>
            ) : (
              <>
                <Button size="sm" variant="ghost" icon="edit" onClick={backToSetup} disabled={applying}>
                  修改任务
                </Button>
                <Button size="sm" icon="refresh" onClick={rerun} disabled={applying}>
                  重新生成
                </Button>
              </>
            )}
          </div>
        </div>
        {s.run?.task && (
          <div className="org-run-task" title={s.run.task}>
            <p>{s.run.task}</p>
          </div>
        )}
        <ol className="org-steps" aria-live="polite">
          {s.steps.map((item) => (
            <li key={item.id} className={item.tone}>
              <span>{item.text}</span>
              {item.note && <span className="org-step-note">{item.note}</span>}
            </li>
          ))}
        </ol>
      </Card>

      {s.proposals.length > 0 && (
        <>
          <SectionHead title="修改方案" note={`选中 ${s.selected.size} / ${s.proposals.length} 项`}>
            <Button size="sm" variant="ghost" onClick={toggleAll} disabled={!reviewing || applying}>
              {allOn ? "全不选" : "全选"}
            </Button>
          </SectionHead>
          <List className="org-props">
            {s.proposals.map((p, i) => (
              <ProposalRow
                key={`${i}-${p.path}`}
                proposal={p}
                on={s.selected.has(i)}
                open={expanded.has(i)}
                locked={!reviewing || applying}
                onToggle={() => toggle(i)}
                onExpand={() => expand(i)}
                onOpenNote={(path) => ctx.openNote(path)}
              />
            ))}
          </List>
        </>
      )}

      {reviewing && !s.proposals.length && !s.error && !s.stopped && (
        <EmptyState icon="check-circle" title="知识库已经很整齐了" actions={<Button icon="edit" onClick={backToSetup}>换个任务</Button>}>
          AI 看过了这次的任务，认为不需要改动任何文件。
        </EmptyState>
      )}

      {s.applyError && (
        <div className="org-error" role="alert">
          <Icon name="alert-circle" size={14} />
          <span>{s.applyError}</span>
        </div>
      )}

      {reviewing && s.proposals.length > 0 && (
        <div className="org-apply">
          <span className="org-foot-note">
            <Icon name="info" size={12} />
            写入前会再核对一次：生成之后又被改过的笔记不会被覆盖。
          </span>
          <Button variant="ghost" onClick={discard} disabled={applying}>
            放弃
          </Button>
          <Button variant="primary" icon="check" onClick={() => applySelected(ctx)} disabled={!s.selected.size} loading={applying}>
            {applying ? "写入中…" : `应用选中的 ${s.selected.size} 项`}
          </Button>
        </div>
      )}
    </>
  );

  const extra = Math.max(0, s.touched.length - s.applied.length);
  const doneView = (
    <Card padded={false} className="org-done">
      <div className="org-done-head">
        <span className="org-done-ic">
          <Icon name="check" size={18} stroke={2} />
        </span>
        <div className="org-done-words">
          <b>已写入 {s.touched.length} 个文件，并提交到 git</b>
          <span>{s.summary || "可以在「版本与同步」里查看这次提交。"}</span>
        </div>
      </div>
      <List className="org-touched">
        {s.applied.map((p, i) => {
          const target = p.op === "move" ? p.to : p.path;
          const gone = p.op === "delete";
          return (
            <Row
              key={`${i}-${p.path}`}
              compact
              icon={gone ? "trash" : p.op === "move" ? "move" : p.op === "create" ? "file-plus" : "file-text"}
              title={noteTitle(target)}
              sub={`${OP_LABEL[p.op] || p.op}${parentFolder(target) ? ` · ${parentFolder(target)}` : ""}`}
              chevron={!gone}
              disabled={gone}
              onClick={gone ? undefined : () => ctx.openNote(target)}
            />
          );
        })}
      </List>
      <div className="org-done-foot">
        {extra > 0 && <span className="org-foot-note">另有 {extra} 个文件随之更新（链接、对话状态）。</span>}
        <span className="grow" />
        <Button variant="ghost" icon="git-branch" onClick={() => ctx.go("sync")}>
          版本与同步
        </Button>
        <Button variant="primary" icon="sparkles" onClick={reset}>
          再整理一次
        </Button>
      </div>
    </Card>
  );

  return (
    <Page className="org">
      {topbar}
      <PageIntro>
        让 AI 按 <b>AGENTS.md</b> 的约定整理知识库：先给出可审阅的修改方案，你确认后才会写入，并自动记录到 git 历史。
      </PageIntro>
      {aiMissing && (
        <div className="org-notice" role="status">
          <Icon name="alert" size={14} />
          <span className="grow">还没有配置 AI 模型，整理需要一个可用的模型。</span>
          <Button size="sm" onClick={() => ctx.go("settings", "ai")}>
            去设置
          </Button>
        </div>
      )}
      {s.phase === "setup" ? setupView : s.phase === "done" ? doneView : runView}
    </Page>
  );
}
