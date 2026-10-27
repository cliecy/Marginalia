/**
 * The sidebar's information architecture: groups of pages, each page's label,
 * icon and the little signals at its right (a count, a dot). Settings sits in
 * the footer with its own sub-navigation.
 *
 * Predicates receive the AppContext value (ctx).
 */
export const NAV_GROUPS = [
  {
    id: "notes",
    label: "笔记",
    items: [
      { id: "home", label: "首页", icon: "home" },
      { id: "notes", label: "笔记", icon: "file-text" },
      { id: "chats", label: "对话", icon: "message", count: (ctx) => ctx.inbox || null, countTitle: "待整理的对话" },
      { id: "graph", label: "知识图谱", icon: "graph" },
    ],
  },
  {
    id: "ai",
    label: "AI",
    items: [
      { id: "organize", label: "整理知识库", icon: "sparkles" },
      { id: "prompts", label: "提示词", icon: "prompt" },
    ],
  },
  {
    id: "sync",
    label: "同步",
    items: [
      {
        id: "sync",
        label: "版本与同步",
        icon: "git-branch",
        // a blue dot when something waits on the reader: a failed sync, commits not pushed or pulled
        dot: (ctx) => {
          const git = ctx.git;
          if (!git?.enabled || !git.remote) return null;
          if (git.last_sync?.ok === false) return { tone: "red", title: "上次同步失败" };
          if (git.ahead || git.behind) return { tone: "accent", title: git.ahead ? `${git.ahead} 个提交待推送` : `远端有 ${git.behind} 个新提交` };
          return null;
        },
      },
    ],
  },
  {
    id: "connect",
    label: "接入",
    items: [
      { id: "api", label: "API 与 MCP", icon: "plug" },
      {
        id: "magpie",
        label: "Magpie",
        icon: "magpie",
        dot: (ctx) => (ctx.integrations?.magpie?.gateway?.running ? { tone: "green", title: "Magpie 网关运行中" } : null),
      },
    ],
  },
];

/** Settings, in the footer, and its sections (sub-navigation shown while it is open). */
export const SETTINGS_NAV = {
  id: "settings",
  label: "设置",
  icon: "settings",
  subs: [
    { id: "general", label: "常规" },
    { id: "vault", label: "笔记库" },
    { id: "ai", label: "AI 模型" },
    { id: "api", label: "API" },
    { id: "git", label: "Git" },
    { id: "about", label: "关于" },
  ],
};

/** Looks up a page's group and item by view id. */
export function findNav(view) {
  if (view === SETTINGS_NAV.id) return { group: null, item: SETTINGS_NAV };
  for (const group of NAV_GROUPS) {
    const item = group.items.find((i) => i.id === view);
    if (item) return { group, item };
  }
  return { group: null, item: null };
}

/** The sub-sections of a view (only Settings has them). */
export function subsFor(view, ctx) {
  if (view !== SETTINGS_NAV.id) return [];
  return SETTINGS_NAV.subs.filter((s) => !s.when || s.when(ctx));
}
