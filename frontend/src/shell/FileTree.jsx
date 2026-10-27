import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useApp } from "../app/AppContext.jsx";
import { useStoredState } from "../app/hooks.js";
import { api } from "../lib/api.js";
import { copyText } from "../ui/CopyField.jsx";
import { IconButton } from "../ui/Button.jsx";
import EmptyState from "../ui/EmptyState.jsx";
import { SearchInput } from "../ui/Field.jsx";
import Icon from "../ui/Icon.jsx";
import { isMac } from "../lib/native.js";
import { contextMenu, MenuButton, openMenu } from "../ui/Popover.jsx";
import Spinner from "../ui/Spinner.jsx";
import { toast } from "../ui/Toast.jsx";

const IMAGE = /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i;

const label = (node) => (node.type === "note" ? node.name.replace(/\.md$/i, "") : node.name);
const fileIcon = (node) => (node.type === "note" ? "file-text" : IMAGE.test(node.name) ? "image" : /\.pdf$/i.test(node.name) ? "book" : "file");
const parentOf = (path) => path.split("/").slice(0, -1).join("/");

/** Keeps the folders (and their ancestors) whose own name or any descendant's matches. */
function filterTree(nodes, query) {
  const out = [];
  for (const node of nodes) {
    const hit = node.name.toLowerCase().includes(query);
    if (node.type === "folder") {
      const children = filterTree(node.children || [], query);
      if (hit || children.length) out.push({ ...node, children: hit && !children.length ? node.children || [] : children, matched: hit });
    } else if (hit) out.push(node);
  }
  return out;
}

/** Collects every folder path (to show a filtered tree fully expanded). */
function folderPaths(nodes, out = new Set()) {
  for (const node of nodes) {
    if (node.type === "folder") {
      out.add(node.path);
      folderPaths(node.children || [], out);
    }
  }
  return out;
}

/** Wraps the matched part of a name in <mark>. */
function Highlight({ text, query }) {
  if (!query) return text;
  const index = text.toLowerCase().indexOf(query);
  if (index < 0) return text;
  return (
    <>
      {text.slice(0, index)}
      <mark>{text.slice(index, index + query.length)}</mark>
      {text.slice(index + query.length)}
    </>
  );
}

/**
 * One row of the tree. Keyboard: Enter/Space open, ←/→ fold, ↑/↓ move,
 * Shift+F10 or the menu key opens the row's menu, F2 renames, Delete
 * (⌘⌫ on a Mac) deletes.
 */
function TreeRow({ node, depth, current, expanded, query, onToggle, onOpen, menuFor, onRename, onDelete }) {
  const isFolder = node.type === "folder";
  const open = isFolder && expanded.has(node.path);
  const active = !isFolder && current === node.path;
  const onKeyDown = (event) => {
    if (event.target !== event.currentTarget) return;
    const row = event.currentTarget;
    if (event.key === "ContextMenu" || (event.key === "F10" && event.shiftKey)) {
      event.preventDefault();
      openMenu(row, menuFor(node), { placement: "bottom-start", mark: row });
    } else if (event.key === "F2" && !event.metaKey && !event.ctrlKey && !event.altKey) {
      event.preventDefault();
      onRename(node);
    } else if (event.key === "Delete" || (isMac && event.key === "Backspace" && event.metaKey)) {
      event.preventDefault();
      onDelete(node);
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      isFolder ? onToggle(node.path) : onOpen(node.path);
    } else if (event.key === "ArrowRight" && isFolder && !open) {
      event.preventDefault();
      onToggle(node.path);
    } else if (event.key === "ArrowLeft" && isFolder && open) {
      event.preventDefault();
      onToggle(node.path);
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const rows = [...event.currentTarget.closest(".tree").querySelectorAll(".tree-row")];
      const index = rows.indexOf(event.currentTarget);
      rows[index + (event.key === "ArrowDown" ? 1 : -1)]?.focus();
    }
  };
  return (
    <>
      <div
        className={`tree-row ${isFolder ? "folder" : node.type} ${active ? "active" : ""}`.trim()}
        role="treeitem"
        aria-expanded={isFolder ? open : undefined}
        aria-selected={active || undefined}
        aria-level={depth + 1}
        tabIndex={0}
        data-path={node.path}
        style={{ "--depth": depth }}
        title={node.path}
        onClick={() => (isFolder ? onToggle(node.path) : onOpen(node.path))}
        onKeyDown={onKeyDown}
        onContextMenu={(event) => contextMenu(event, menuFor(node))}
      >
        <span className={`tree-chev ${open ? "open" : ""}`.trim()} aria-hidden="true">
          {isFolder && <Icon name="chevron-right" size={12} stroke={1.7} />}
        </span>
        <Icon name={isFolder ? (open ? "folder-open" : "folder") : fileIcon(node)} size={15} className="tree-ic" />
        <span className="tree-name">
          <Highlight text={label(node)} query={query} />
        </span>
        <button
          type="button"
          className="tree-more"
          tabIndex={-1}
          aria-label="更多操作"
          onClick={(event) => {
            event.stopPropagation();
            openMenu(event.currentTarget, menuFor(node), { placement: "bottom-end", mark: event.currentTarget.closest(".tree-row") });
          }}
        >
          <Icon name="more" size={14} />
        </button>
      </div>
      {open && (
        <div role="group" className="tree-group">
          {(node.children || []).map((child) => (
            <TreeRow key={child.path} node={child} depth={depth + 1} current={current} expanded={expanded} query={query} onToggle={onToggle} onOpen={onOpen} menuFor={menuFor} onRename={onRename} onDelete={onDelete} />
          ))}
        </div>
      )}
    </>
  );
}

/**
 * The notes workspace's file column: a filter field (Enter runs a full-text
 * search), new note / new folder, and the vault tree with chevrons and a
 * context menu (new here, rename / move, reveal, copy link, upload, delete).
 *
 * @param {object} props
 * @param {string|null} props.current  the open note's path
 * @param {(path: string) => void} props.onOpen
 */
export default function FileTree({ current, onOpen }) {
  const ctx = useApp();
  const { tree, treeLoaded } = ctx;
  const [query, setQuery] = useState("");
  const [results, setResults] = useState(null);
  const [searching, setSearching] = useState(false);
  const [stored, setStored] = useStoredState("mn.expanded", []);
  const expanded = useMemo(() => new Set(stored), [stored]);
  const input = useRef(null);
  const body = useRef(null);
  const upload = useRef(null);
  const uploadTarget = useRef("");

  const setExpanded = useCallback((update) => setStored((prev) => [...update(new Set(prev))]), [setStored]);
  const toggle = useCallback(
    (path) =>
      setExpanded((set) => {
        set.has(path) ? set.delete(path) : set.add(path);
        return set;
      }),
    [setExpanded],
  );
  const expandTo = useCallback(
    (path, includeSelf = false) => {
      const parts = path.split("/").filter(Boolean);
      const upto = includeSelf ? parts.length : parts.length - 1;
      if (upto <= 0) return;
      setExpanded((set) => {
        for (let i = 1; i <= upto; i++) set.add(parts.slice(0, i).join("/"));
        return set;
      });
    },
    [setExpanded],
  );

  // Reveal the open note's folder.
  useEffect(() => {
    if (current?.includes("/")) expandTo(current);
  }, [current, expandTo]);

  // Scroll the open note into view once it is in the tree.
  useEffect(() => {
    if (!current || query) return;
    const frame = requestAnimationFrame(() => body.current?.querySelector(`.tree-row[data-path="${CSS.escape(current)}"]`)?.scrollIntoView({ block: "nearest" }));
    return () => cancelAnimationFrame(frame);
  }, [current, tree, query]);

  // ctx.revealInTree(path): expand to it, scroll, flash.
  useEffect(() => {
    const request = ctx.treeReveal;
    if (!request) return;
    setQuery("");
    setResults(null);
    if (!request.path) {
      body.current?.scrollTo({ top: 0, behavior: "smooth" });
      return;
    }
    expandTo(request.path, true);
    const timer = setTimeout(() => {
      const row = body.current?.querySelector(`.tree-row[data-path="${CSS.escape(request.path)}"]`);
      if (!row) return;
      row.scrollIntoView({ block: "center", behavior: "smooth" });
      row.classList.remove("flash");
      void row.offsetWidth;
      row.classList.add("flash");
    }, 60);
    return () => clearTimeout(timer);
  }, [ctx.treeReveal, expandTo]);

  // ctx.focusSearch(q): focus the field (filled with q when given).
  useEffect(() => {
    const request = ctx.searchRequest;
    if (!request) return;
    if (request.q) {
      setQuery(request.q);
      runSearch(request.q);
    }
    const frame = requestAnimationFrame(() => {
      input.current?.focus();
      input.current?.select();
    });
    return () => cancelAnimationFrame(frame);
  }, [ctx.searchRequest]); // eslint-disable-line react-hooks/exhaustive-deps

  const runSearch = async (text) => {
    const q = text.trim();
    if (!q) return setResults(null);
    setSearching(true);
    try {
      const r = await api.search(q);
      setResults({ q, hits: r.results || [] });
    } catch (err) {
      toast.err(`搜索失败：${err.message}`);
    } finally {
      setSearching(false);
    }
  };

  const changeQuery = (text) => {
    setQuery(text);
    if (!text.trim()) setResults(null);
  };

  const needle = query.trim().toLowerCase();
  const filtered = useMemo(() => (needle ? filterTree(tree, needle) : tree), [tree, needle]);
  const shownExpanded = useMemo(() => (needle ? folderPaths(filtered) : expanded), [needle, filtered, expanded]);

  const menuFor = (node) => {
    const isFolder = node.type === "folder";
    const folder = isFolder ? node.path : parentOf(node.path);
    const items = [
      !isFolder && { label: "打开", icon: "file-text", onSelect: () => onOpen(node.path) },
      { label: "在此新建笔记", icon: "file-plus", onSelect: () => ctx.newNote(folder) },
      isFolder && { label: "新建子文件夹", icon: "folder-plus", onSelect: () => ctx.newFolder(node.path) },
      isFolder && {
        label: "上传文件到这里…",
        icon: "upload",
        onSelect: () => {
          uploadTarget.current = node.path;
          upload.current?.click();
        },
      },
      { divider: true },
      { label: "重命名 / 移动…", icon: "edit", onSelect: () => ctx.renamePath(node.path) },
      node.type === "note" && { label: "复制双链", icon: "link", onSelect: () => copyText(`[[${label(node)}]]`).then(() => toast.ok("已复制双链")) },
      { label: "复制路径", icon: "copy", onSelect: () => copyText(node.path).then(() => toast.ok("已复制路径")) },
      ctx.host.native && { label: "在 Finder 中显示", icon: "folder-open", onSelect: () => ctx.revealPath(node.path) },
      { divider: true },
      {
        label: isFolder ? "删除文件夹" : "删除",
        icon: "trash",
        danger: true,
        // Only empty folders can be deleted (by either server).
        disabled: isFolder && !!node.children?.length,
        hint: isFolder && node.children?.length ? "先移走或删除里面的文件" : undefined,
        onSelect: () => deleteNode(node),
      },
    ];
    return items.filter(Boolean);
  };

  const renameNode = (node) => ctx.renamePath(node.path);
  const deleteNode = (node) => {
    const isFolder = node.type === "folder";
    if (isFolder && node.children?.length) return toast.warn("文件夹不是空的，先移走或删除里面的文件");
    return ctx.deletePath(node.path, { folder: isFolder });
  };

  const rootMenu = () =>
    [
      {
        label: "上传文件…",
        icon: "upload",
        hint: ctx.me.assets_dir ? `存到 ${ctx.me.assets_dir}/` : undefined,
        onSelect: () => {
          uploadTarget.current = "";
          upload.current?.click();
        },
      },
      { label: "全部折叠", icon: "chevrons-up-down", onSelect: () => setStored([]) },
      { label: "刷新", icon: "refresh", onSelect: () => ctx.reloadTree() },
      ctx.host.native && { divider: true },
      ctx.host.native && { label: "在 Finder 中显示笔记库", icon: "folder-open", onSelect: () => ctx.revealPath("") },
    ].filter(Boolean);

  return (
    <div className="ftree">
      <div className="ftree-head">
        <SearchInput
          ref={input}
          value={query}
          onChange={changeQuery}
          onEnter={runSearch}
          onEscape={() => (query ? changeQuery("") : input.current?.blur())}
          placeholder="筛选笔记，回车全文搜索"
          size="sm"
          aria-label="搜索笔记"
        />
        <IconButton icon="file-plus" label="新建笔记" size="sm" onClick={() => ctx.newNote("")} />
        <IconButton icon="folder-plus" label="新建文件夹" size="sm" onClick={() => ctx.newFolder("")} />
        <MenuButton items={rootMenu} label="更多" size="sm" />
      </div>
      <div className="ftree-body" ref={body}>
        {results ? (
          <div className="hits">
            <div className="hits-head">
              <span>
                「{results.q}」· {results.hits.length} 条结果
              </span>
              <button type="button" className="link-btn" onClick={() => changeQuery("")}>
                返回目录
              </button>
            </div>
            {results.hits.length === 0 ? (
              <EmptyState compact icon="search" title="没有找到">
                换个关键词试试
              </EmptyState>
            ) : (
              results.hits.map((hit) => (
                <button key={hit.path} type="button" className={`hit ${current === hit.path ? "active" : ""}`.trim()} onClick={() => onOpen(hit.path)}>
                  <span className="hit-title">{hit.path.split("/").pop().replace(/\.md$/i, "")}</span>
                  <span className="hit-path">{hit.path}</span>
                  {(hit.snippets || []).slice(0, 2).map((s, i) => (
                    <span key={`${s.line}-${i}`} className="hit-snippet">
                      <Highlight text={s.text} query={results.q.toLowerCase()} />
                    </span>
                  ))}
                </button>
              ))
            )}
          </div>
        ) : (
          <div className="tree" role="tree" aria-label="笔记目录">
            {!treeLoaded ? (
              <div className="tree-loading">
                <Spinner />
              </div>
            ) : filtered.length === 0 ? (
              needle ? (
                <EmptyState compact icon="search" title="没有匹配的文件名">
                  按回车在正文中搜索「{query.trim()}」
                </EmptyState>
              ) : (
                <EmptyState compact icon="folder" title="笔记库还是空的" actions={<button type="button" className="link-btn" onClick={() => ctx.newNote("")}>新建第一篇笔记</button>} />
              )
            ) : (
              filtered.map((node) => (
                <TreeRow key={node.path} node={node} depth={0} current={current} expanded={shownExpanded} query={needle} onToggle={toggle} onOpen={onOpen} menuFor={menuFor} onRename={renameNode} onDelete={deleteNode} />
              ))
            )}
          </div>
        )}
        {searching && (
          <div className="ftree-busy">
            <Spinner size={12} /> 搜索中…
          </div>
        )}
      </div>
      <input
        ref={upload}
        type="file"
        multiple
        hidden
        onChange={(event) => {
          const list = event.target.files;
          if (list?.length) ctx.uploadFiles(list, uploadTarget.current || undefined);
          event.target.value = "";
        }}
      />
    </div>
  );
}
