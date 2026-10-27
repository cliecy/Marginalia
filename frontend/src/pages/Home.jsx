import { useEffect, useMemo, useState } from "react";
import "./Home.css";
import { useApp } from "../app/AppContext.jsx";
import { useAsync } from "../app/hooks.js";
import { formatCount, noteTitle, parentFolder, timeAgo } from "../app/format.js";
import { api } from "../lib/api.js";
import { TopbarActions } from "../shell/Topbar.jsx";
import { Badge, Button, EmptyState, Icon, IconButton, Kbd, List, ListFoot, Page, Row, SearchInput, SectionHead, Skeleton, Stats } from "../ui/index.js";

/** Folders whose notes are not "your notes" (conversations, plugin data, templates). */
const AUXILIARY = ["Chats/", "copilot/", "_templates/"];
const RECENT_STEP = 8;
const RECENT_MAX = 32;
const WEEKDAYS = ["日", "一", "二", "三", "四", "五", "六"];

/** git's sync stamps end in "+0900" (strftime %z); WebKit's Date only reads "+09:00". */
const stamp = (value) => (typeof value === "string" ? value.replace(/([+-]\d{2})(\d{2})$/, "$1:$2") : value);

/** "早上好" … "夜深了" for the hour of `date`. */
function greetingFor(date) {
  const hour = date.getHours();
  if (hour < 5) return "夜深了";
  if (hour < 11) return "早上好";
  if (hour < 13) return "中午好";
  if (hour < 18) return "下午好";
  if (hour < 23) return "晚上好";
  return "夜深了";
}

/** Re-renders every `ms` so relative times ("3 分钟前") stay true while the page is open. */
function useNow(ms = 60000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(timer);
  }, [ms]);
  return now;
}

/** Counts notes and folders and lists notes by modification time, newest first. */
function useVaultFacts(tree) {
  return useMemo(() => {
    const notes = [];
    let folders = 0;
    const visit = (nodes) => {
      for (const node of nodes) {
        if (node.type === "folder") {
          folders += 1;
          visit(node.children || []);
        } else if (node.type === "note") {
          notes.push(node);
        }
      }
    };
    visit(tree);
    const own = notes.filter((n) => !AUXILIARY.some((prefix) => n.path.startsWith(prefix)));
    const recent = own.filter((n) => Number.isFinite(n.mtime)).sort((a, b) => b.mtime - a.mtime);
    return { noteCount: own.length, folderCount: folders, recent };
  }, [tree]);
}

/** One quick-action card: a tinted glyph tile, a title, two lines of explanation, a foot line. */
function ActionCard({ icon, tone = "neutral", title, children, foot, onClick, index }) {
  return (
    <button type="button" className={`home-act home-act-${tone}`} onClick={onClick} style={{ "--i": index }}>
      <span className="home-act-top">
        <span className="home-act-ic">
          <Icon name={icon} size={18} />
        </span>
        <Icon name="arrow-up-right" size={14} className="home-act-go" />
      </span>
      <b className="home-act-title">{title}</b>
      <span className="home-act-text">{children}</span>
      {foot != null && <span className="home-act-foot">{foot}</span>}
    </button>
  );
}

/** Placeholder rows while the tree loads. */
function RecentSkeleton() {
  return (
    <List aria-busy="true">
      {[0, 1, 2, 3].map((i) => (
        <div key={i} className="row home-skel-row">
          <Skeleton width={26} height={26} radius={7} />
          <div className="home-skel-who">
            <Skeleton width={`${46 - i * 6}%`} height={10} />
            <Skeleton width={`${28 + i * 4}%`} height={8} />
          </div>
          <Skeleton width={52} height={8} />
        </div>
      ))}
    </List>
  );
}

/**
 * 首页 — the dashboard: a greeting with a search field, the vault in four
 * figures, quick actions, the notes touched most recently and the chats
 * waiting to be distilled.
 *
 * Props: { route, sub } — everything else comes from useApp().
 */
export default function Home() {
  const ctx = useApp();
  const now = useNow();
  const { noteCount, folderCount, recent } = useVaultFacts(ctx.tree);
  const [shown, setShown] = useState(RECENT_STEP);
  const [query, setQuery] = useState("");

  // Links between notes, for the 双链 figure (the graph endpoint already counts them).
  const graph = useAsync(() => api.graph(false), [ctx.tree]);
  const linkFacts = useMemo(() => {
    const data = graph.data;
    if (!data) return null;
    const linked = new Set();
    for (const edge of data.edges || []) {
      linked.add(edge.source);
      linked.add(edge.target);
    }
    return { links: (data.edges || []).length, orphans: (data.nodes || []).filter((n) => !linked.has(n.id)).length };
  }, [graph.data]);

  // Refresh the shared data when the dashboard opens (the tree too: saving a
  // note doesn't reload it, and 最近修改 sorts by its modification times).
  useEffect(() => {
    ctx.reloadTree();
    ctx.reloadInbox();
    ctx.reloadGit();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const today = new Date(now);
  const user = ctx.me?.user || "";
  const vaultName = ctx.me?.vault?.name || "";
  const inboxChats = useMemo(() => (ctx.chats || []).filter((c) => c.status === "inbox"), [ctx.chats]);
  const hasMap = ctx.files.includes("index.md");
  const inboxFolder = ctx.tree.some((n) => n.type === "folder" && n.name === "Inbox") ? "Inbox" : "";

  const reload = () => {
    ctx.reloadTree();
    ctx.reloadInbox();
    ctx.reloadGit();
  };

  const search = (text) => {
    const q = text.trim();
    if (q) ctx.focusSearch(q);
  };

  // ------------------------------------------------------------ figures
  const git = ctx.git;
  const lastSync = git?.last_sync;
  let syncStat;
  if (!git) syncStat = { label: "上次同步", value: "—", hint: "git 状态不可用" };
  else if (!git.enabled) syncStat = { label: "上次同步", value: "未启用", hint: "笔记库没有启用 git" };
  else if (!git.remote) syncStat = { label: "上次同步", value: "仅本地", hint: "未配置远端仓库" };
  else if (!lastSync) syncStat = { label: "上次同步", value: "尚未同步", hint: git.ahead ? `${git.ahead} 个提交待推送` : "本次启动后还没有同步" };
  else if (lastSync.ok === false) syncStat = { label: "上次同步", value: timeAgo(stamp(lastSync.at), now) || "失败", hint: "同步失败，点开看原因", tone: "red" };
  else syncStat = { label: "上次同步", value: timeAgo(stamp(lastSync.at), now) || "—", hint: git.ahead ? `${git.ahead} 个提交待推送` : "已是最新" };

  const stats = [
    {
      label: "笔记",
      value: ctx.treeLoaded ? formatCount(noteCount) : "—",
      hint: `${formatCount(folderCount)} 个文件夹`,
      onClick: () => ctx.go("notes"),
    },
    {
      label: "待整理对话",
      value: ctx.chats ? formatCount(ctx.inbox) : "—",
      hint: ctx.chats ? `共 ${formatCount(ctx.chats.length)} 个对话` : "正在读取",
      tone: ctx.inbox ? "accent" : undefined,
      onClick: () => ctx.go("chats"),
    },
    {
      label: "双链",
      value: linkFacts ? formatCount(linkFacts.links) : "—",
      hint: linkFacts ? (linkFacts.orphans ? `${formatCount(linkFacts.orphans)} 篇笔记还没有链接` : "每篇笔记都连上了") : graph.error ? "暂时无法统计" : "正在统计",
      onClick: () => ctx.go("graph"),
    },
    { ...syncStat, onClick: () => ctx.go("sync") },
  ];

  const visible = recent.slice(0, shown);
  const more = Math.min(recent.length, RECENT_MAX) - visible.length;
  const vaultEmpty = ctx.treeLoaded && noteCount === 0;

  return (
    <Page className="home-page">
      <TopbarActions>
        <IconButton icon="refresh" label="刷新" onClick={reload} />
        <Button variant="soft" icon="plus" onClick={() => ctx.newNote(inboxFolder)}>
          新建笔记
        </Button>
      </TopbarActions>

      <section className="home-hero">
        <div className="home-hero-text">
          <h1>
            {greetingFor(today)}
            {user ? `，${user}` : ""}
          </h1>
          <p>
            {today.getMonth() + 1}月{today.getDate()}日 星期{WEEKDAYS[today.getDay()]}
            {vaultName ? (
              <>
                <i aria-hidden="true">·</i>笔记库 <b>{vaultName}</b>
              </>
            ) : null}
            {ctx.inbox ? (
              <>
                <i aria-hidden="true">·</i>
                {ctx.inbox} 个对话等你沉淀
              </>
            ) : null}
          </p>
        </div>
        <div className="home-hero-search">
          <SearchInput value={query} onChange={setQuery} onEnter={search} placeholder="搜索笔记全文，回车查找" aria-label="搜索笔记" />
          <Kbd keys="Mod+K" />
        </div>
      </section>

      <Stats items={stats} className="home-stats" />

      <SectionHead title="快捷操作" />
      <div className="home-acts">
        <ActionCard index={0} icon="sparkles" tone="accent" title="和 AI 聊聊" onClick={() => ctx.openChatInPanel(null)} foot="对话会自动存进 Chats/">
          在笔记旁打开 AI 面板，可以带上当前笔记或整个知识库一起问。
        </ActionCard>
        <ActionCard
          index={1}
          icon="inbox"
          tone="green"
          title="整理收件箱"
          onClick={() => ctx.startOrganize({ preset: "inbox" })}
          foot={ctx.inbox ? <Badge tone="green">{ctx.inbox} 个待整理</Badge> : "收件箱是空的"}
        >
          把对话里有长期价值的内容沉淀成主题笔记，确认后才写入。
        </ActionCard>
        <ActionCard index={2} icon="graph" tone="sky" title="知识图谱" onClick={() => ctx.go("graph")} foot={linkFacts ? `${formatCount(linkFacts.links)} 条双链` : null}>
          看看笔记之间如何相连，找出还没接上的孤岛。
        </ActionCard>
        {hasMap ? (
          <ActionCard index={3} icon="book-open" tone="amber" title="知识地图" onClick={() => ctx.openNote("index.md")} foot="index.md">
            知识库的总目录：按主题列出所有笔记，从这里开始逛。
          </ActionCard>
        ) : (
          <ActionCard index={3} icon="layers" tone="amber" title="初始化结构" onClick={() => ctx.startOrganize({ preset: "migrate" })} foot="还没有 index.md">
            按 AGENTS.md 的约定归置文件夹、补全 frontmatter，并生成知识地图。
          </ActionCard>
        )}
      </div>

      <SectionHead title="最近修改" note={ctx.treeLoaded && recent.length ? `${formatCount(recent.length)} 篇笔记` : null}>
        <Button size="sm" variant="ghost" iconRight="chevron-right" onClick={() => ctx.go("notes")}>
          全部笔记
        </Button>
      </SectionHead>
      {!ctx.treeLoaded ? (
        <RecentSkeleton />
      ) : vaultEmpty ? (
        <List>
          <EmptyState
            compact
            icon="file-text"
            title="笔记库还是空的"
            actions={
              <>
                <Button icon="plus" onClick={() => ctx.newNote(inboxFolder)}>
                  新建笔记
                </Button>
                <Button variant="ghost" icon="layers" onClick={() => ctx.startOrganize({ preset: "migrate" })}>
                  初始化结构
                </Button>
              </>
            }
          >
            写下第一篇笔记，或者让 AI 按约定搭好文件夹和知识地图。
          </EmptyState>
        </List>
      ) : (
        <List className="home-recent">
          {visible.map((note) => {
            const folder = parentFolder(note.path);
            return (
              <Row
                key={note.path}
                icon="file-text"
                title={noteTitle(note.path)}
                sub={folder || "根目录"}
                meta={<span title={new Date(note.mtime * 1000).toLocaleString()}>{timeAgo(note.mtime, now)}</span>}
                chevron
                onClick={() => ctx.openNote(note.path)}
              />
            );
          })}
          {more > 0 && <ListFoot onClick={() => setShown((n) => n + RECENT_STEP)}>再显示 {Math.min(more, RECENT_STEP)} 篇</ListFoot>}
        </List>
      )}

      {inboxChats.length > 0 && (
        <>
          <SectionHead title="待整理的对话" note={`${inboxChats.length} 个`}>
            <Button size="sm" variant="ghost" iconRight="chevron-right" onClick={() => ctx.go("chats")}>
              全部对话
            </Button>
          </SectionHead>
          <List>
            {inboxChats.slice(0, 3).map((chat) => (
              <Row
                key={chat.path}
                icon="message"
                title={chat.title}
                sub={chat.updated ? `更新于 ${timeAgo(chat.updated, now)}` : chat.path}
                onClick={() => ctx.openChatInPanel(chat.path)}
                right={
                  <Button size="sm" variant="soft" icon="sparkles" onClick={() => ctx.startOrganize({ preset: "inbox", chats: [chat.path] })}>
                    沉淀
                  </Button>
                }
              />
            ))}
            {inboxChats.length > 3 && <ListFoot onClick={() => ctx.go("chats")}>还有 {inboxChats.length - 3} 个待整理</ListFoot>}
          </List>
        </>
      )}
    </Page>
  );
}
