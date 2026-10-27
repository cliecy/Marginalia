import { useEffect, useMemo, useState } from "react";
import "./Prompts.css";
import { useApp } from "../app/AppContext.jsx";
import { useAsync, useStoredState } from "../app/hooks.js";
import { formatCount } from "../app/format.js";
import { api } from "../lib/api.js";
import { TopbarActions } from "../shell/Topbar.jsx";
import { Button, EmptyState, Icon, IconButton, List, MenuButton, Page, PageIntro, Row, SearchInput, Skeleton, copyText, toast } from "../ui/index.js";

/** Where Obsidian Copilot (and the AI panel) look for prompt templates. */
const PROMPTS_DIR = "copilot/copilot-custom-prompts";

/** The template variables the AI panel fills in. */
const VARIABLES = {
  "{}": { label: "选中的文字", hint: "AI 面板里选中的文字；没有选中时是整篇笔记" },
  "{activeNote}": { label: "当前笔记", hint: "当前打开的笔记全文" },
};
const VARIABLE_RE = /(\{\}|\{activeNote\})/g;

/** When (ms) the template is offered to the AI panel, which may still be mounting. */
const OFFER_DELAYS = [60, 260, 640];

/**
 * The first line of a template that says something, for the row's sub line:
 * body text rather than a heading ("## 任务"), with the variables named.
 */
function firstLine(text) {
  const lines = String(text || "")
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
  const line = lines.find((l) => !/^#{1,6}\s/.test(l)) ?? lines[0]?.replace(/^#+\s*/, "");
  if (!line) return "（空模板）";
  return line.replace(VARIABLE_RE, (v) => `「${VARIABLES[v].label}」`);
}

/** Which variables a template uses. */
const variablesOf = (text) => Object.keys(VARIABLES).filter((v) => String(text || "").includes(v));

/** The template text with its variables drawn as tokens. */
function TemplateText({ text }) {
  const parts = String(text || "").split(VARIABLE_RE);
  return (
    <pre className="pr-template">
      {parts.map((part, index) =>
        VARIABLES[part] ? (
          <span key={index} className="pr-var" title={`${part} — ${VARIABLES[part].hint}`}>
            {VARIABLES[part].label}
          </span>
        ) : (
          part
        ),
      )}
    </pre>
  );
}

/** Placeholder rows while the templates load. */
function PromptSkeleton() {
  return (
    <div className="pr-layout">
      <List aria-busy="true">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="row pr-skel">
            <Skeleton width={26} height={26} radius={7} />
            <div className="pr-skel-who">
              <Skeleton width={`${54 - i * 7}%`} height={10} />
              <Skeleton width={`${72 - i * 6}%`} height={8} />
            </div>
          </div>
        ))}
      </List>
      <div className="pr-preview pr-preview-skel">
        <Skeleton width="40%" height={14} />
        <Skeleton width="62%" height={9} />
        <Skeleton width="100%" height={120} radius={9} />
      </div>
    </div>
  );
}

/**
 * 提示词 — the prompt templates of the vault (copilot/copilot-custom-prompts,
 * shared with Obsidian Copilot): a list on the left, the chosen template on the
 * right with its variables drawn as tokens, and actions to use it in the AI
 * panel, copy it or edit the file.
 *
 * Props: { route, sub }. Data: api.prompts() → { prompts: [{ name, path, template }] }.
 */
export default function Prompts() {
  const ctx = useApp();
  const { data, error, loading, reload } = useAsync(() => api.prompts(), []);
  const prompts = useMemo(() => [...(data?.prompts || [])].sort((a, b) => a.name.localeCompare(b.name, "zh-Hans-CN")), [data]);
  const [selectedPath, setSelectedPath] = useStoredState("mn.prompts.selected", null);
  const [query, setQuery] = useState("");

  // Pick up templates created or edited elsewhere (the editor, Obsidian, an agent).
  useEffect(() => ctx.events.on("vault:changed", () => reload()), [ctx.events, reload]);
  useEffect(() => ctx.events.on("note:saved", ({ path } = {}) => path?.startsWith(`${PROMPTS_DIR}/`) && reload()), [ctx.events, reload]);

  const q = query.trim().toLowerCase();
  const shown = useMemo(() => (q ? prompts.filter((p) => `${p.name}\n${p.template}`.toLowerCase().includes(q)) : prompts), [prompts, q]);
  const selected = shown.find((p) => p.path === selectedPath) || shown[0] || null;

  const newPrompt = () => ctx.newNote(PROMPTS_DIR);

  /**
   * Opens the AI panel and offers it the template: the panel listens for
   * "ai:use-prompt" on ctx.events, runs it and calls `ack()`. The offer is
   * repeated for a moment, since the notes workspace (and its panel) may still
   * be mounting; if nobody takes it (the panel is busy answering), the reader
   * is told where to pick the template themselves.
   */
  const openInPanel = (prompt) => {
    ctx.openInspector("ai");
    let taken = false;
    const payload = { name: prompt.name, path: prompt.path, template: prompt.template, ack: () => (taken = true) };
    for (const delay of OFFER_DELAYS) setTimeout(() => !taken && ctx.events.emit("ai:use-prompt", payload), delay);
    setTimeout(() => {
      if (!taken) toast(`在 AI 面板的「我的提示词」里选择「${prompt.name}」即可使用`, { duration: 4200 });
    }, OFFER_DELAYS[OFFER_DELAYS.length - 1] + 150);
  };

  const copyTemplate = async (prompt) => {
    if (await copyText(prompt.template)) toast("已复制模板");
  };

  const menuFor = (prompt) => () => [
    { label: "在 AI 面板中使用", icon: "sparkles", onSelect: () => openInPanel(prompt) },
    { label: "编辑模板", icon: "edit", onSelect: () => ctx.openNote(prompt.path, { mode: "edit" }) },
    { label: "复制模板", icon: "copy", onSelect: () => copyTemplate(prompt) },
    { divider: true },
    { label: "重命名…", icon: "move", onSelect: async () => (await ctx.renamePath(prompt.path)) && reload() },
    { label: "删除…", icon: "trash", danger: true, onSelect: async () => (await ctx.deletePath(prompt.path)) && reload() },
  ];

  let body;
  if (loading && !data) {
    body = <PromptSkeleton />;
  } else if (error && !data) {
    body = (
      <EmptyState icon="alert-circle" title="读取提示词失败" actions={<Button icon="refresh" onClick={reload}>重试</Button>}>
        {error.message}
      </EmptyState>
    );
  } else if (prompts.length === 0) {
    body = (
      <EmptyState
        icon="prompt"
        title="还没有提示词模板"
        actions={
          <Button variant="primary" icon="plus" onClick={newPrompt}>
            新建提示词
          </Button>
        }
      >
        在 {PROMPTS_DIR}/ 下新建一篇笔记，正文就是模板：<code>{"{}"}</code> 代表选中的文字，<code>{"{activeNote}"}</code> 代表当前笔记。和 Obsidian Copilot 插件通用。
      </EmptyState>
    );
  } else {
    const vars = selected ? variablesOf(selected.template) : [];
    body = (
      <div className="pr-layout">
        <div className="pr-side">
          {prompts.length > 6 && <SearchInput size="sm" value={query} onChange={setQuery} placeholder="搜索提示词" aria-label="搜索提示词" className="pr-search" />}
          {shown.length === 0 ? (
            <List>
              <EmptyState compact icon="search" title={`没有找到「${query.trim()}」`} />
            </List>
          ) : (
            <List className="pr-list" aria-label="提示词">
              {shown.map((prompt) => (
                <Row
                  key={prompt.path}
                  compact
                  icon="prompt"
                  title={prompt.name}
                  sub={firstLine(prompt.template)}
                  active={selected?.path === prompt.path}
                  onClick={() => setSelectedPath(prompt.path)}
                  onDoubleClick={() => openInPanel(prompt)}
                />
              ))}
            </List>
          )}
        </div>

        {selected && (
          <article className="pr-preview" key={selected.path}>
            <header className="pr-head">
              <div className="pr-head-who">
                <h2>{selected.name}</h2>
                <span className="pr-path" title={selected.path}>
                  {selected.path}
                </span>
              </div>
              <div className="pr-head-actions">
                <IconButton icon="copy" label="复制模板" onClick={() => copyTemplate(selected)} />
                <IconButton icon="edit" label="编辑模板" onClick={() => ctx.openNote(selected.path, { mode: "edit" })} />
                <MenuButton label="更多" items={menuFor(selected)} />
              </div>
            </header>

            <TemplateText text={selected.template} />

            <footer className="pr-foot">
              <div className="pr-vars">
                {vars.length ? (
                  vars.map((v) => (
                    <span key={v} className="pr-var-note">
                      <span className="pr-var">{VARIABLES[v].label}</span>
                      {VARIABLES[v].hint}
                    </span>
                  ))
                ) : (
                  <span className="pr-var-note">没有变量：模板会原样发给 AI。</span>
                )}
                <span className="pr-count">{formatCount(selected.template.length)} 字</span>
              </div>
              <Button variant="primary" icon="sparkles" onClick={() => openInPanel(selected)}>
                在 AI 面板中使用
              </Button>
            </footer>
          </article>
        )}
      </div>
    );
  }

  return (
    <Page className="pr-page" width="wide">
      <TopbarActions>
        <IconButton icon="refresh" label="刷新" onClick={reload} />
        <Button variant="soft" icon="plus" onClick={newPrompt}>
          新建提示词
        </Button>
      </TopbarActions>
      <PageIntro>
        常用的指令写成模板，在 AI 面板里一键使用。模板放在 <b>{PROMPTS_DIR}/</b>，和 Obsidian Copilot 通用。
        {prompts.length > 0 && <span className="pr-intro-count"> 共 {prompts.length} 个。</span>}
      </PageIntro>
      {body}
      {prompts.length > 0 && (
        <p className="pr-tip">
          <Icon name="info" size={12} />
          双击列表里的提示词也可以直接使用。
        </p>
      )}
    </Page>
  );
}
