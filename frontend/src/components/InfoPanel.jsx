import { useEffect, useMemo, useRef, useState } from "react";
import { useApp } from "../app/AppContext.jsx";
import { api } from "../lib/api.js";
import { outline } from "../lib/markdown.js";
import { formatDateTime, noteTitle, parentFolder, timeAgo } from "../app/format.js";
import Button, { IconButton } from "../ui/Button.jsx";
import EmptyState from "../ui/EmptyState.jsx";
import Icon from "../ui/Icon.jsx";
import { Skeleton } from "../ui/Page.jsx";
import Spinner from "../ui/Spinner.jsx";
import { toast } from "../ui/Toast.jsx";
import DiffView, { diffStats } from "./DiffView.jsx";

/** A few shimmering rows while a list loads. */
function LoadingRows({ rows = 4 }) {
  return (
    <div className="info-loading" aria-busy="true" aria-label="加载中">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="info-skel">
          <Skeleton width={`${72 - i * 9}%`} height={10} />
          <Skeleton width={`${44 - i * 4}%`} height={8} />
        </div>
      ))}
    </div>
  );
}

/** The small heading over a list: what it is and how many. */
function InfoHead({ title, count, children }) {
  return (
    <div className="info-head">
      <span className="info-title">{title}</span>
      {count != null && <span className="info-count">{count}</span>}
      {children && <span className="info-head-actions">{children}</span>}
    </div>
  );
}

/** Splits a backlink line around its [[wikilinks]] so they can be marked. */
function LinkLine({ text }) {
  const parts = text.split(/(!?\[\[[^\]\n]+\]\])/g);
  return (
    <span className="backlink-line">
      {parts.map((part, i) => (/^!?\[\[/.test(part) ? <mark key={i}>{part.replace(/^!?\[\[|\]\]$/g, "").split("|").pop()}</mark> : part))}
    </span>
  );
}

// ----------------------------------------------------------------- outline
function Outline({ content, onHeading }) {
  const items = useMemo(() => outline(content), [content]);
  if (!items.length) {
    return (
      <EmptyState compact icon="list" title="这篇笔记没有标题">
        用 # 开头的行写标题，这里会列出大纲。
      </EmptyState>
    );
  }
  const min = Math.min(...items.map((i) => i.level));
  return (
    <>
      <InfoHead title="大纲" count={items.length} />
      <nav className="outline" aria-label="大纲">
        {items.map((item) => {
          const depth = item.level - min;
          return (
            <button
              key={item.id}
              type="button"
              className={`outline-item lv-${Math.min(depth, 3)}`}
              style={{ "--depth": depth }}
              title={item.text}
              onClick={() => onHeading?.(item)}
            >
              {Array.from({ length: depth }, (_, i) => (
                <i key={i} className="outline-guide" style={{ "--guide": i }} aria-hidden="true" />
              ))}
              <span>{item.text}</span>
            </button>
          );
        })}
      </nav>
    </>
  );
}

// --------------------------------------------------------------- backlinks
function Backlinks({ path, refreshKey, onOpen }) {
  const [state, setState] = useState({ list: null, error: null });

  useEffect(() => {
    let live = true;
    setState((s) => ({ list: s.list, error: null }));
    api
      .backlinks(path)
      .then((r) => live && setState({ list: r.backlinks || [], error: null }))
      .catch((err) => live && setState({ list: [], error: err.message }));
    return () => {
      live = false;
    };
  }, [path, refreshKey]);

  if (state.error) {
    return (
      <EmptyState compact icon="alert-circle" title="反链读取失败">
        {state.error}
      </EmptyState>
    );
  }
  if (!state.list) return <LoadingRows rows={3} />;
  if (!state.list.length) {
    return (
      <EmptyState compact icon="backlink" title="还没有反链">
        其他笔记用 [[{noteTitle(path)}]] 链接到这里时，会出现在这儿。
      </EmptyState>
    );
  }
  return (
    <>
      <InfoHead title="链接到这里" count={state.list.length} />
      <div className="backlinks">
        {state.list.map((b) => (
          <button key={b.path} type="button" className="backlink" onClick={() => onOpen?.(b.path)} title={b.path}>
            <span className="backlink-head">
              <Icon name="file-text" size={14} />
              <b>{noteTitle(b.path)}</b>
              {parentFolder(b.path) && <span className="backlink-dir">{parentFolder(b.path)}</span>}
            </span>
            {(b.lines || []).slice(0, 3).map((line, i) => (
              <LinkLine key={i} text={line} />
            ))}
          </button>
        ))}
      </div>
    </>
  );
}

// ----------------------------------------------------------------- history
/** When edits get committed, from git.commit_delay_seconds (vaguer when the server doesn't say). */
function commitDelayText(seconds) {
  const value = seconds == null || seconds === "" ? NaN : Number(seconds);
  if (!Number.isFinite(value) || value < 0) return "编辑后稍等片刻会自动提交到 git";
  if (value < 1) return "编辑后会立即自动提交到 git";
  if (value < 90) return `编辑约 ${Math.round(value)} 秒后会自动提交到 git`;
  return `编辑约 ${Math.round(value / 60)} 分钟后会自动提交到 git`;
}

function History({ path, content, refreshKey, onRestore }) {
  const ctx = useApp();
  const [state, setState] = useState({ list: null, error: null });
  const [version, setVersion] = useState(null);
  const [opening, setOpening] = useState(null);
  const seq = useRef(0);

  useEffect(() => {
    setVersion(null);
    setOpening(null);
  }, [path]);

  useEffect(() => {
    let live = true;
    setState((s) => ({ list: s.list, error: null }));
    api
      .history(path)
      .then((r) => live && setState({ list: r.history || [], error: null }))
      .catch((err) => live && setState({ list: [], error: err.message }));
    return () => {
      live = false;
    };
  }, [path, refreshKey]);

  const open = async (entry) => {
    const id = ++seq.current;
    setOpening(entry.rev);
    try {
      const result = await api.version(path, entry.rev);
      // Compared with the editor's text, which always has \n line ends.
      if (id === seq.current) setVersion({ entry, content: String(result.content ?? "").replace(/\r\n/g, "\n") });
    } catch (err) {
      if (id === seq.current) toast.err(`读取这个版本失败：${err.message}`);
    } finally {
      if (id === seq.current) setOpening(null);
    }
  };

  if (version) {
    const stats = diffStats(version.content, content);
    const same = stats.added === 0 && stats.removed === 0;
    return (
      <div className="history-version">
        <div className="history-bar">
          <IconButton icon="chevron-left" size="sm" label="返回历史列表" onClick={() => setVersion(null)} />
          <div className="history-bar-words">
            <b>{version.entry.subject || "提交"}</b>
            <span>
              {formatDateTime(version.entry.date)} · {version.entry.author}
            </span>
          </div>
          <Button size="sm" variant="soft" icon="history" disabled={same} onClick={() => onRestore?.(version.content)}>
            恢复
          </Button>
        </div>
        <p className="history-note">
          {same ? (
            "这个版本和现在的内容一样。"
          ) : (
            <>
              相对现在的内容：<em className="add">+{stats.added}</em> <em className="del">−{stats.removed}</em> 行。− 是这个版本有、现在没有的。
            </>
          )}
        </p>
        <DiffView before={version.content} after={content} maxHeight="none" className="history-diff" />
      </div>
    );
  }

  if (state.error) {
    return (
      <EmptyState compact icon="alert-circle" title="历史读取失败">
        {state.error}
      </EmptyState>
    );
  }
  if (!state.list) return <LoadingRows />;
  if (!state.list.length) {
    if (!ctx.me?.git) {
      // No repository, or auto-commit is off: nothing will ever show up here by itself.
      return (
        <EmptyState
          compact
          icon="history"
          title="没有启用 git"
          actions={
            <Button size="sm" onClick={() => ctx.go("settings", "git")}>
              打开 Git 设置
            </Button>
          }
        >
          笔记库启用 git 自动提交后，每次编辑都会留下版本，可以在这里对比和恢复。
        </EmptyState>
      );
    }
    return (
      <EmptyState compact icon="history" title="还没有历史版本">
        {commitDelayText(ctx.settings?.git?.commit_delay_seconds)}，之后可以在这里对比和恢复。
      </EmptyState>
    );
  }
  return (
    <>
      <InfoHead title="历史版本" count={state.list.length} />
      <ol className="history-list">
        {state.list.map((entry) => (
          <li key={entry.rev}>
            <button type="button" className="history-row" onClick={() => open(entry)} disabled={opening === entry.rev} title={`${formatDateTime(entry.date)} · ${entry.rev}`}>
              <i className="history-dot" aria-hidden="true" />
              <span className="history-words">
                <span className="history-title">{entry.subject || "提交"}</span>
                <span className="history-sub">
                  {timeAgo(entry.date)} · {entry.author}
                </span>
              </span>
              {opening === entry.rev ? <Spinner size={12} /> : <code className="history-rev">{String(entry.rev).slice(0, 7)}</code>}
            </button>
          </li>
        ))}
      </ol>
    </>
  );
}

/**
 * The inspector's non-AI tabs for the open note: its outline, the notes that
 * link to it, and its git history (with a diff against the current text and
 * a restore).
 *
 * @param {object} props
 * @param {"outline"|"backlinks"|"history"} props.tab
 * @param {string|null} props.path       the open note (null: nothing open)
 * @param {string} props.content         its current text
 * @param {number} [props.refreshKey]    bump to reload backlinks / history
 * @param {(path: string) => void} [props.onOpen]
 * @param {(item: {id, text, level, line}) => void} [props.onHeading]
 * @param {(text: string) => void} [props.onRestore]
 */
export default function InfoPanel({ tab, path, content, onHeading, onOpen, onRestore, refreshKey }) {
  if (!path) {
    return (
      <EmptyState compact icon="file-text" title="没有打开的笔记">
        打开一篇笔记后，这里会显示它的大纲、反链和历史。
      </EmptyState>
    );
  }
  if (tab === "outline") return <Outline content={content} onHeading={onHeading} />;
  if (tab === "backlinks") return <Backlinks path={path} refreshKey={refreshKey} onOpen={onOpen} />;
  return <History path={path} content={content} refreshKey={refreshKey} onRestore={onRestore} />;
}
