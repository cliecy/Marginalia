import { useEffect, useRef, useState } from "react";
import "./Settings.css";
import { useApp } from "../app/AppContext.jsx";
import { useEvent, useWindowEvent } from "../app/hooks.js";
import { TopbarActions } from "../shell/Topbar.jsx";
import { SETTINGS_NAV, subsFor } from "../shell/nav.js";
import { Button, EmptyState, Page, PageIntro, SearchInput } from "../ui/index.js";
import About from "./settings/About.jsx";
import Ai from "./settings/Ai.jsx";
import ApiSettings from "./settings/ApiSettings.jsx";
import { SettingsSkeleton } from "./settings/controls.jsx";
import General from "./settings/General.jsx";
import Git from "./settings/Git.jsx";
import { SettingsQuery } from "./settings/search.jsx";
import Vault from "./settings/Vault.jsx";

/**
 * Each section: its component, whether it needs /api/app/settings, and the
 * muted line under the top bar (a string, or a function of the app context).
 */
const SECTIONS = {
  general: { Component: General, needsSettings: false, intro: null },
  vault: { Component: Vault, needsSettings: true, intro: "笔记库就是一个装着 Markdown 文件的文件夹：Marginalia 直接读写它，不会把笔记搬进别的地方。" },
  ai: { Component: Ai, needsSettings: true, intro: "AI 对话和整理知识库用的模型从这里来：Magpie 的本地网关，或者任何 OpenAI 兼容的服务。" },
  api: {
    Component: ApiSettings,
    needsSettings: true,
    intro: (ctx) =>
      ctx.host.desktop
        ? "本地 API 让 Claude Code、Codex 和你自己的脚本读写知识库：REST 给脚本，MCP 给 AI 工具。"
        : "外部程序带着访问令牌就能读写这台服务器上的知识库：REST 给脚本，MCP 给 AI 工具。",
  },
  git: { Component: Git, needsSettings: true, intro: "每一次修改都记在 git 里，可以随时找回；配置好远端后还能在几台设备之间同步。" },
  about: { Component: About, needsSettings: false, intro: null },
};

const LABELS = Object.fromEntries(SETTINGS_NAV.subs.map((s) => [s.id, s.label]));

/**
 * 设置 — Magpie-style settings: rows with a label, a line of explanation and
 * the control at the right, one section at a time (the sub-navigation lives
 * in the sidebar), and a 查找设置 field in the top bar that searches across
 * every section.
 *
 * @param {object} props
 * @param {string} props.sub  "general" | "vault" | "ai" | "api" | "git" | "about"
 */
export default function Settings({ sub = "general" }) {
  const ctx = useApp();
  const [query, setQuery] = useState("");
  const search = useRef(null);
  const [status, setStatus] = useState(ctx.settings ? "ready" : "loading");

  // Fresh settings on every visit (the listener state, values changed by menus or other windows).
  const load = useEvent(async () => {
    if (!ctx.settings) setStatus("loading");
    ctx.reloadIntegrations();
    const settings = await ctx.reloadSettings();
    setStatus(settings ? "ready" : "error");
  });
  useEffect(() => {
    load();
  }, [load]);

  // ⌘F finds a setting, as in Magpie.
  useWindowEvent("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === "f") {
      event.preventDefault();
      search.current?.focus();
      search.current?.select();
    }
  });

  const allowed = subsFor("settings", ctx).map((s) => s.id);
  // An unknown section (an old link) shows 常规, as the breadcrumb does.
  const section = allowed.includes(sub) ? sub : "general";
  const q = query.trim().toLowerCase();
  const rawIntro = SECTIONS[section].intro;
  const intro = typeof rawIntro === "function" ? rawIntro(ctx) : rawIntro;

  const body = (id) => {
    const { Component, needsSettings } = SECTIONS[id];
    if (needsSettings && !ctx.settings) {
      if (q) return null;
      if (status === "error") {
        return (
          <div className="list">
            <EmptyState compact icon="alert-circle" title="读不到设置" actions={<Button size="sm" icon="refresh" onClick={load}>重试</Button>}>
              应用的设置接口没有回应，稍后再试一次。
            </EmptyState>
          </div>
        );
      }
      return <SettingsSkeleton rows={id === "git" ? 3 : 2} />;
    }
    return <Component />;
  };

  return (
    <Page className="set-page">
      <TopbarActions>
        <SearchInput ref={search} size="sm" value={query} onChange={setQuery} placeholder="查找设置" className="set-q" aria-label="查找设置" />
      </TopbarActions>

      <SettingsQuery.Provider value={q}>
        {q ? (
          <div className="set-results">
            {allowed.map((id) => (
              <section key={id} className="set-part" data-part={LABELS[id]}>
                {body(id)}
              </section>
            ))}
            <p className="set-none">没有找到和「{query.trim()}」有关的设置</p>
          </div>
        ) : (
          <div key={section} className="set-section">
            {intro && <PageIntro>{intro}</PageIntro>}
            {body(section)}
          </div>
        )}
      </SettingsQuery.Provider>
    </Page>
  );
}
