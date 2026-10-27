import { useEffect, useMemo, useReducer, useState } from "react";
import "./Chats.css";
import { useApp } from "../app/AppContext.jsx";
import { useStoredState } from "../app/hooks.js";
import { formatCount, formatDateTime, timeAgo } from "../app/format.js";
import { api } from "../lib/api.js";
import { TopbarActions } from "../shell/Topbar.jsx";
import { Button, EmptyState, Icon, IconButton, List, ListFoot, MenuButton, Page, PageIntro, Row, SearchInput, Segmented, Skeleton, StatusDot, copyText, toast } from "../ui/index.js";

const PAGE_SIZE = 40;
const DETAIL_CONCURRENCY = 4;
const TRANSCRIPT_MESSAGES = 24;
const MESSAGE_CHARS = 1400;

// ---------------------------------------------------------------- details
// /api/chats lists path, title, status and updated only; the model, the
// message count and the opening question come from each chat file. They are
// fetched for the rows on screen, a few at a time, and kept for the session
// (keyed by path + updated, so an edited chat is read again).

/** @type {Map<string, ChatDetail>} */
const detailCache = new Map();
const pending = new Map();
let running = 0;
const waiting = [];

const keyOf = (chat) => `${chat.path}\u0000${chat.updated || ""}`;
const oneLine = (text) => String(text || "").replace(/\s+/g, " ").trim();
const clip = (text, max) => (text.length > max ? `${text.slice(0, max).trimEnd()}…` : text);

/** Waits for a free loading slot (at most DETAIL_CONCURRENCY requests at once). */
const acquire = () => (running < DETAIL_CONCURRENCY ? (running += 1, Promise.resolve()) : new Promise((resolve) => waiting.push(resolve)));
const release = () => {
  const next = waiting.shift();
  if (next) next();
  else running -= 1;
};

/** Wiki links in `distilled_into` ("[[Knowledge/PPO]]") → vault paths without brackets. */
function linkTargets(value) {
  const items = Array.isArray(value) ? value : value ? [value] : [];
  return items.map((item) => String(item).replace(/^\[\[|\]\]$/g, "").split("|")[0].trim()).filter(Boolean);
}

/**
 * @typedef {object} ChatDetail
 * @property {string} model
 * @property {number} count       number of messages
 * @property {string} excerpt     the first question, on one line
 * @property {string} created
 * @property {string[]} distilledInto
 * @property {{role: string, content: string}[]} messages  the first messages, clipped
 * @property {boolean} [failed]
 */

/** Reduces /api/chats/load to what the list shows. */
function summarize(loaded) {
  const meta = loaded?.meta || {};
  const messages = loaded?.messages || [];
  const first = messages.find((m) => m.role === "user");
  return {
    model: meta.model ? String(meta.model) : "",
    count: messages.length,
    excerpt: clip(oneLine(first?.content), 140),
    created: meta.created ? String(meta.created) : "",
    distilledInto: linkTargets(meta.distilled_into),
    messages: messages.slice(0, TRANSCRIPT_MESSAGES).map((m) => ({ role: m.role, content: clip(String(m.content || "").trim(), MESSAGE_CHARS) })),
  };
}

/** Loads (once) the detail of a chat; resolves to the cached ChatDetail. */
function loadDetail(chat) {
  const key = keyOf(chat);
  if (detailCache.has(key)) return Promise.resolve(detailCache.get(key));
  if (!pending.has(key)) {
    const job = acquire()
      .then(() => api.loadChat(chat.path))
      .then(summarize, () => ({ model: "", count: 0, excerpt: "", created: "", distilledInto: [], messages: [], failed: true }))
      .then((detail) => {
        detailCache.set(key, detail);
        pending.delete(key);
        release();
        return detail;
      });
    pending.set(key, job);
  }
  return pending.get(key);
}

/**
 * The details of the given chats, loading those not seen yet. Returns a
 * lookup `(chat) => ChatDetail | undefined`; the component re-renders as
 * details arrive (answers landing together cause one render).
 */
function useChatDetails(chats) {
  const [, bump] = useReducer((n) => n + 1, 0);
  const signature = chats.map(keyOf).join("\n");
  useEffect(() => {
    let live = true;
    let timer = 0;
    const refresh = () => {
      if (!live || timer) return;
      timer = setTimeout(() => {
        timer = 0;
        if (live) bump();
      }, 16);
    };
    for (const chat of chats) if (!detailCache.has(keyOf(chat))) loadDetail(chat).then(refresh);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [signature]); // eslint-disable-line react-hooks/exhaustive-deps
  return (chat) => detailCache.get(keyOf(chat)) ?? (typeof chat.messages === "number" ? { model: chat.model || "", count: chat.messages, excerpt: "", distilledInto: [], messages: null } : undefined);
}

/** Re-renders every minute so "3 分钟前" stays true. */
function useMinuteTick() {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60000);
    return () => clearInterval(timer);
  }, []);
  return now;
}

/** Marks every occurrence of `query` in `text` (case-insensitive). */
function Highlight({ text, query }) {
  if (!query) return text;
  const lower = text.toLowerCase();
  const needle = query.toLowerCase();
  const parts = [];
  let from = 0;
  for (let at = lower.indexOf(needle); at >= 0; at = lower.indexOf(needle, from)) {
    if (at > from) parts.push(text.slice(from, at));
    parts.push(<mark key={at}>{text.slice(at, at + needle.length)}</mark>);
    from = at + needle.length;
  }
  if (from < text.length) parts.push(text.slice(from));
  return parts;
}

// ---------------------------------------------------------------- rows

/** The opened row: facts, where it was distilled to, the conversation, actions. */
function ChatDetailPanel({ chat, detail, onContinue, onDistill, onOpenFile, onOpenNote, resolve }) {
  if (!detail) {
    return (
      <div className="chats-detail">
        <div className="chats-talk chats-talk-loading">
          <Skeleton width="62%" height={10} />
          <Skeleton width="44%" height={10} />
          <Skeleton width="54%" height={10} />
        </div>
      </div>
    );
  }
  const messages = detail.messages || [];
  const hidden = detail.count - messages.length;
  return (
    <div className="chats-detail">
      <dl className="chats-facts">
        {detail.created && (
          <div>
            <dt>开始</dt>
            <dd>{formatDateTime(detail.created)}</dd>
          </div>
        )}
        {chat.updated && (
          <div>
            <dt>更新</dt>
            <dd>{formatDateTime(chat.updated)}</dd>
          </div>
        )}
        {detail.model && (
          <div>
            <dt>模型</dt>
            <dd className="mono">{detail.model}</dd>
          </div>
        )}
        <div className="chats-fact-path">
          <dt>文件</dt>
          <dd className="mono" title={chat.path}>
            {chat.path}
          </dd>
        </div>
      </dl>

      {detail.distilledInto.length > 0 && (
        <div className="chats-distilled">
          <span className="chats-distilled-label">
            <Icon name="check-circle" size={13} />
            已沉淀到
          </span>
          {detail.distilledInto.map((target) => {
            const path = resolve(target);
            return (
              <button key={target} type="button" className="chats-note-link" disabled={!path} title={path || `${target}（找不到这篇笔记）`} onClick={() => path && onOpenNote(path)}>
                <Icon name="file-text" size={12} />
                {target.split("/").pop()}
              </button>
            );
          })}
        </div>
      )}

      {detail.failed ? (
        <p className="chats-detail-note">读取这个对话失败了，可以打开原文看看。</p>
      ) : messages.length === 0 ? (
        <p className="chats-detail-note">这个对话还没有消息。</p>
      ) : (
        <div className="chats-talk">
          {messages.map((message, index) => (
            <div key={index} className={`chats-msg chats-msg-${message.role === "user" ? "user" : "ai"}`}>
              <span className="chats-msg-who">{message.role === "user" ? "我" : "AI"}</span>
              <div className="chats-msg-body">{message.content || "（空）"}</div>
            </div>
          ))}
          {hidden > 0 && (
            <button type="button" className="chats-talk-more" onClick={onOpenFile}>
              还有 {hidden} 条消息 · 打开原文
            </button>
          )}
        </div>
      )}

      <div className="chats-detail-actions">
        <Button size="sm" icon="message" onClick={onContinue}>
          继续聊
        </Button>
        <Button size="sm" variant={chat.status === "inbox" ? "primary" : "default"} icon="sparkles" onClick={onDistill}>
          {chat.status === "inbox" ? "沉淀为笔记" : "再次沉淀"}
        </Button>
        <span className="grow" />
        <Button size="sm" variant="ghost" icon="file-text" onClick={onOpenFile}>
          打开原文
        </Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- page

/**
 * 对话 — every conversation with the AI, in Magpie's sessions-list style: a
 * segmented filter (收件箱 · 已整理 · 全部) with counts, a search field, one
 * card of rows (title, model · messages · first question, time). A row opens
 * in place to show the conversation; its actions continue it in the AI panel,
 * distill it with the organize agent, or open the Markdown file.
 *
 * Props: { route, sub }. Data: ctx.chats (/api/chats), ctx.inbox.
 */
export default function Chats() {
  const ctx = useApp();
  const now = useMinuteTick();
  const all = useMemo(() => ctx.chats || [], [ctx.chats]);
  const loaded = ctx.chats != null;
  const [storedFilter, setStoredFilter] = useStoredState("mn.chats.filter", "inbox");
  const [chosenFilter, setChosenFilter] = useState(null);
  const [query, setQuery] = useState("");
  const [shown, setShown] = useState(PAGE_SIZE);
  const [open, setOpen] = useState(null);
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState(() => new Set());
  const [refreshing, setRefreshing] = useState(false);

  useEffect(() => {
    ctx.reloadInbox();
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const counts = useMemo(() => {
    const inbox = all.filter((c) => c.status === "inbox").length;
    return { inbox, done: all.length - inbox, all: all.length };
  }, [all]);

  // With an empty inbox the page opens on 全部 rather than on an empty list
  // (only when it opens: a 收件箱 the reader picks stays picked).
  useEffect(() => {
    if (loaded && chosenFilter === null) setChosenFilter(storedFilter === "inbox" && counts.inbox === 0 && counts.all > 0 ? "all" : storedFilter);
  }, [loaded]); // eslint-disable-line react-hooks/exhaustive-deps
  const effectiveFilter = chosenFilter ?? storedFilter;
  const setFilter = (value) => {
    setChosenFilter(value);
    setStoredFilter(value);
    setOpen(null);
    setSelected(new Set());
  };

  const byStatus = useMemo(() => {
    if (effectiveFilter === "inbox") return all.filter((c) => c.status === "inbox");
    if (effectiveFilter === "done") return all.filter((c) => c.status !== "inbox");
    return all;
  }, [all, effectiveFilter]);

  // Search runs over the title and path, and over the model and first question once those are loaded.
  const q = query.trim().toLowerCase();
  const matches = useMemo(() => {
    if (!q) return byStatus;
    return byStatus.filter((chat) => {
      const detail = detailCache.get(keyOf(chat));
      const hay = `${chat.title}\n${chat.path}\n${detail?.model || ""}\n${detail?.excerpt || ""}`.toLowerCase();
      return hay.includes(q);
    });
  }, [byStatus, q]);
  const visible = matches.slice(0, shown);
  const detailOf = useChatDetails(visible);

  useEffect(() => setShown(PAGE_SIZE), [effectiveFilter, q]);
  useEffect(() => {
    if (!selecting) setSelected(new Set());
  }, [selecting]);

  /** A distilled_into target → an existing vault path (or null). */
  const resolve = (target) => {
    for (const candidate of [target, `${target}.md`]) if (ctx.files.includes(candidate)) return candidate;
    const name = `${target.split("/").pop()}.md`;
    return ctx.files.find((f) => f === name || f.endsWith(`/${name}`)) || null;
  };

  const refresh = async () => {
    setRefreshing(true);
    await ctx.reloadInbox();
    setRefreshing(false);
  };

  const distill = (paths) => ctx.startOrganize({ preset: "inbox", chats: paths });

  const remove = async (chat) => {
    if (await ctx.deletePath(chat.path)) {
      if (open === chat.path) setOpen(null);
    }
  };

  const toggleSelected = (path) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });

  const menuFor = (chat) => () => [
    { label: "继续聊", icon: "message", onSelect: () => ctx.openChatInPanel(chat.path) },
    { label: chat.status === "inbox" ? "沉淀为笔记" : "再次沉淀", icon: "sparkles", onSelect: () => distill([chat.path]) },
    { label: "打开原文", icon: "file-text", onSelect: () => ctx.openNote(chat.path) },
    { divider: true },
    {
      label: "复制路径",
      icon: "copy",
      onSelect: async () => {
        await copyText(chat.path);
        toast("已复制路径");
      },
    },
    ...(ctx.host.desktop ? [{ label: "在 Finder 中显示", icon: "folder-open", onSelect: () => ctx.revealPath(chat.path) }] : []),
    { divider: true },
    { label: "删除…", icon: "trash", danger: true, onSelect: () => remove(chat) },
  ];

  const allVisibleSelected = visible.length > 0 && visible.every((c) => selected.has(c.path));

  // ------------------------------------------------------------ render
  let body;
  if (!loaded) {
    body = (
      <List aria-busy="true">
        {[0, 1, 2, 3, 4].map((i) => (
          <div key={i} className="row chats-skel">
            <div className="chats-skel-who">
              <Skeleton width={`${38 - i * 3}%`} height={10} />
              <Skeleton width={`${58 - i * 5}%`} height={8} />
            </div>
            <Skeleton width={46} height={8} />
          </div>
        ))}
      </List>
    );
  } else if (all.length === 0) {
    body = (
      <EmptyState
        icon="message"
        title="还没有对话"
        actions={
          <Button variant="primary" icon="sparkles" onClick={() => ctx.openChatInPanel(null)}>
            开始新对话
          </Button>
        }
      >
        在笔记旁边打开 AI 面板聊天，对话会自动保存到 Chats/。其他 AI 工具也可以通过 MCP 的 save_conversation 存进来。
      </EmptyState>
    );
  } else if (matches.length === 0) {
    body = q ? (
      <List>
        <EmptyState compact icon="search" title={`没有找到「${query.trim()}」`} actions={<Button size="sm" onClick={() => setQuery("")}>清除搜索</Button>}>
          {effectiveFilter !== "all" ? "试试在「全部」里找找。" : "换个关键词试试。"}
        </EmptyState>
      </List>
    ) : effectiveFilter === "inbox" ? (
      <EmptyState icon="inbox" title="收件箱是空的">
        所有对话都已经沉淀成笔记了。新的对话会先来到这里。
      </EmptyState>
    ) : (
      <EmptyState icon="check-circle" title="还没有整理过的对话">
        在收件箱里选一个对话「沉淀」，整理完的对话会出现在这里。
      </EmptyState>
    );
  } else {
    body = (
      <List className={`chats-list ${selecting ? "selecting" : ""}`.trim()}>
        {selecting && (
          <div className="chats-bar">
            <input
              type="checkbox"
              checked={allVisibleSelected}
              aria-label="全选"
              onChange={() => setSelected(allVisibleSelected ? new Set() : new Set(visible.map((c) => c.path)))}
            />
            <span className="chats-bar-count">{selected.size ? `已选 ${selected.size} 个` : "选择要沉淀的对话"}</span>
            <span className="grow" />
            <Button size="sm" variant="primary" icon="sparkles" disabled={!selected.size} onClick={() => distill([...selected])}>
              沉淀所选
            </Button>
          </div>
        )}
        {visible.map((chat) => {
          const detail = detailOf(chat);
          const isOpen = !selecting && open === chat.path;
          const inbox = chat.status === "inbox";
          const sub = !detail ? (
            <Skeleton width={180} height={8} className="chats-sub-skel" />
          ) : (
            <>
              {detail.model && (
                <span className="chats-sub-model">
                  <Highlight text={detail.model} query={query.trim()} />
                </span>
              )}
              <span className="chats-sub-text">{detail.excerpt ? <Highlight text={detail.excerpt} query={query.trim()} /> : detail.failed ? "读取失败" : chat.path}</span>
            </>
          );
          return (
            <div key={chat.path} className={`chats-item ${isOpen ? "open" : ""}`.trim()}>
              <Row
                className="chats-row"
                active={selecting && selected.has(chat.path)}
                icon={
                  selecting ? (
                    <input type="checkbox" className="chats-check" checked={selected.has(chat.path)} tabIndex={-1} readOnly aria-label={`选择「${chat.title}」`} />
                  ) : (
                    <span className="chats-fold" aria-hidden="true">
                      <Icon name="chevron-right" size={13} />
                    </span>
                  )
                }
                title={
                  <>
                    {effectiveFilter !== "inbox" && inbox && <StatusDot tone="accent" ring={false} label="待整理" />}
                    <span className="chats-title">
                      <Highlight text={chat.title} query={query.trim()} />
                    </span>
                  </>
                }
                sub={sub}
                meta={
                  <>
                    <span className="chats-when" title={chat.updated ? formatDateTime(chat.updated) : undefined}>
                      {chat.updated ? timeAgo(chat.updated, now) : "—"}
                    </span>
                    <span className="chats-count">{detail?.count ? `${detail.count} 条消息` : inbox ? "待整理" : "已整理"}</span>
                  </>
                }
                right={
                  selecting ? null : (
                    <div className="chats-acts">
                      {/* an open row carries the same actions in its detail */}
                      {!isOpen && (
                        <Button size="sm" icon="message" onClick={() => ctx.openChatInPanel(chat.path)}>
                          继续聊
                        </Button>
                      )}
                      {!isOpen && inbox && (
                        <Button size="sm" variant="soft" icon="sparkles" onClick={() => distill([chat.path])}>
                          沉淀
                        </Button>
                      )}
                      <MenuButton size="sm" label="更多操作" items={menuFor(chat)} />
                    </div>
                  )
                }
                aria-expanded={selecting ? undefined : isOpen}
                onClick={() => (selecting ? toggleSelected(chat.path) : setOpen(isOpen ? null : chat.path))}
              />
              {isOpen && (
                <ChatDetailPanel
                  chat={chat}
                  detail={detail}
                  resolve={resolve}
                  onContinue={() => ctx.openChatInPanel(chat.path)}
                  onDistill={() => distill([chat.path])}
                  onOpenFile={() => ctx.openNote(chat.path)}
                  onOpenNote={(path) => ctx.openNote(path)}
                />
              )}
            </div>
          );
        })}
        {matches.length > visible.length && (
          <ListFoot onClick={() => setShown((n) => n + PAGE_SIZE)}>显示其余 {formatCount(Math.min(PAGE_SIZE, matches.length - visible.length))} 个</ListFoot>
        )}
      </List>
    );
  }

  const inboxPaths = all.filter((c) => c.status === "inbox").map((c) => c.path);

  return (
    <Page className="chats-page">
      <TopbarActions>
        <IconButton icon="refresh" label="刷新" onClick={refresh} disabled={refreshing} />
        <Button variant="soft" icon="plus" onClick={() => ctx.openChatInPanel(null)}>
          新对话
        </Button>
      </TopbarActions>

      <PageIntro>
        和 AI 的对话都以 Markdown 存在 <b>Chats/</b> 里。收件箱是还没沉淀的对话——把有长期价值的内容整理成知识笔记，闲聊留在这里就好。
      </PageIntro>

      {loaded && all.length > 0 && (
        <div className="chats-tools">
          <Segmented
            value={effectiveFilter}
            onChange={setFilter}
            ariaLabel="筛选对话"
            options={[
              { value: "inbox", label: "收件箱", count: counts.inbox, icon: "inbox" },
              { value: "done", label: "已整理", count: counts.done, icon: "check-circle" },
              { value: "all", label: "全部", count: counts.all },
            ]}
          />
          {effectiveFilter === "inbox" && counts.inbox > 1 && !selecting && (
            <Button size="sm" variant="ghost" icon="sparkles" onClick={() => distill(inboxPaths)} title="把收件箱里的所有对话交给整理知识库">
              全部沉淀
            </Button>
          )}
          <span className="grow" />
          <SearchInput className="chats-search" size="sm" value={query} onChange={setQuery} placeholder="搜索对话" aria-label="搜索对话" />
          <Button size="sm" variant="ghost" icon={selecting ? "x" : "check"} className={selecting ? "chats-select-on" : ""} onClick={() => setSelecting((v) => !v)}>
            {selecting ? "完成" : "选择"}
          </Button>
        </div>
      )}

      {body}

      {loaded && matches.length > 0 && (
        <p className="chats-foot">
          {q ? `找到 ${formatCount(matches.length)} 个对话` : `${formatCount(matches.length)} 个对话`}
          {effectiveFilter === "inbox" ? " · 沉淀后会自动移到「已整理」" : ""}
        </p>
      )}
    </Page>
  );
}
