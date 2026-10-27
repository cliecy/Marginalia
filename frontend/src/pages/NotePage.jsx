import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import "./NotePage.css";
import { useApp } from "../app/AppContext.jsx";
import { useEvent, useStoredState } from "../app/hooks.js";
import { navigate } from "../app/router.js";
import { noteTitle } from "../app/format.js";
import { api, ApiError, fileUrl } from "../lib/api.js";
import Editor from "../components/Editor.jsx";
import Preview from "../components/Preview.jsx";
import AIPanel from "../components/AIPanel.jsx";
import InfoPanel from "../components/InfoPanel.jsx";
import DiffView from "../components/DiffView.jsx";
import Slides from "../components/Slides.jsx";
import FileTree from "../shell/FileTree.jsx";
import { TopbarActions } from "../shell/Topbar.jsx";
import Button, { IconButton } from "../ui/Button.jsx";
import { copyText } from "../ui/CopyField.jsx";
import { Dialog, confirm, prompt } from "../ui/Dialog.jsx";
import EmptyState from "../ui/EmptyState.jsx";
import Icon from "../ui/Icon.jsx";
import { MenuButton } from "../ui/Popover.jsx";
import Segmented from "../ui/Segmented.jsx";
import Spinner from "../ui/Spinner.jsx";
import { toast } from "../ui/Toast.jsx";

const TEXT_EXT = /\.(md|markdown|txt|csv|json|ya?ml|toml|py|sh|tex|bib)$/i;
const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i;

const MODES = [
  { value: "read", label: "阅读", title: "阅读（渲染后的笔记）" },
  { value: "edit", label: "编辑", title: "编辑 Markdown 源码" },
  { value: "split", label: "分屏", title: "左边编辑，右边预览" },
];
const TABS = [
  { value: "ai", label: "AI", title: "AI 助手" },
  { value: "outline", label: "大纲", title: "标题大纲" },
  { value: "backlinks", label: "反链", title: "链接到这篇笔记的其他笔记" },
  { value: "history", label: "历史", title: "git 历史版本" },
];
const STATUS = {
  saved: { label: "已保存", tone: "saved" },
  dirty: { label: "未保存", tone: "dirty" },
  saving: { label: "保存中", tone: "saving" },
  conflict: { label: "有冲突", tone: "conflict" },
  error: { label: "保存失败", tone: "error" },
};

// The workspace's own width (window minus the app sidebar) decides what floats:
// the inspector, then the tree. Split view needs more room, so each pane keeps
// about 380px.
const FLOAT_INSPECTOR = { split: 1380, other: 964 };
const FLOAT_TREE = { split: 1020, other: 684 };

/**
 * The editor and the app keep text with \n line ends; a note that uses \r\n on
 * disk keeps them (converted only when it is read and written).
 */
const toLF = (text) => String(text ?? "").replace(/\r\n/g, "\n");
const eolOf = (text) => (/\r\n/.test(text ?? "") ? "\r\n" : "\n");

const DISTILL_TASK =
  "把这次对话中有长期价值的知识沉淀到知识库：提炼成原子化的主题笔记（已有相关笔记则合并更新），用 [[双链]] 连接相关概念，在 frontmatter 的 sources 中引用这次对话，并更新知识地图。";

/** The save state as a quiet pill; failed or conflicting saves become a button. */
function SaveStatus({ status, onRetry }) {
  const info = STATUS[status] ?? STATUS.saved;
  const content = (
    <>
      {status === "saving" ? <Spinner size={11} /> : <i className="np-save-dot" aria-hidden="true" />}
      <span>{info.label}</span>
    </>
  );
  return status === "error" ? (
    <button type="button" className={`np-save s-${info.tone}`} title="点击重试" onClick={onRetry}>
      {content}
    </button>
  ) : (
    <span className={`np-save s-${info.tone}`} role="status">
      {content}
    </span>
  );
}

/**
 * The notes workspace: file tree | document | inspector (AI · 大纲 · 反链 · 历史).
 *
 * It stays mounted once visited (the shell hides it on other pages) so the
 * editor, scroll positions and the AI conversation survive a look at another
 * page; `active` says whether it is the page showing.
 *
 * Owns the open note's editing: autosave (1.2 s after typing stops, serialized),
 * etag conflicts, failed saves, saving when the page goes away, line endings,
 * applying AI results, creating notes from missing links, uploads and presenting.
 */
export default function NotePage({ active }) {
  const ctx = useApp();
  const { route, me, resolver, files } = ctx;

  const [path, setPath] = useState(null);
  const [content, setContent] = useState("");
  // Changes whenever `content` becomes another document (a note opened from disk):
  // the editor then starts afresh, so undo can never bring back the previous note.
  const [docKey, setDocKey] = useState(0);
  const [status, setStatus] = useState("saved");
  const [loading, setLoading] = useState(false);
  const [storedMode, setMode] = useStoredState("mn.mode", "read");
  const mode = MODES.some((m) => m.value === storedMode) ? storedMode : "read";
  const [storedTab, setTab] = useStoredState("mn.right", "ai");
  const tab = TABS.some((t) => t.value === storedTab) ? storedTab : "ai";
  const [inspectorOpen, setInspectorOpen] = useStoredState("mn.inspector", true);
  const [overlay, setOverlay] = useState({ tree: false, inspector: false });
  const [aiMounted, setAiMounted] = useState(false);
  const [presenting, setPresenting] = useState(false);
  const [confirmDiff, setConfirmDiff] = useState(null);
  const [conflictDiff, setConflictDiff] = useState(null);
  const [refreshKey, setRefreshKey] = useState(0);

  // The workspace's width (0 while another page shows and this one is hidden).
  const root = useRef(null);
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const el = root.current;
    if (!el) return undefined;
    const measure = () => {
      const next = Math.round(el.getBoundingClientRect().width);
      if (next > 0) setWidth(next);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return undefined;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  const roomy = mode === "split" ? "split" : "other";
  const narrowInspector = width > 0 && width < FLOAT_INSPECTOR[roomy];
  const narrowTree = width > 0 && width < FLOAT_TREE[roomy];
  const inspectorShown = narrowInspector ? overlay.inspector : inspectorOpen;
  const treeShown = narrowTree ? overlay.tree : true;

  // Overlays belong to a narrow workspace: once there is room again they close
  // (and don't come back by themselves when it narrows later).
  useEffect(() => {
    if (!narrowInspector) setOverlay((o) => (o.inspector ? { ...o, inspector: false } : o));
  }, [narrowInspector]);
  useEffect(() => {
    if (!narrowTree) setOverlay((o) => (o.tree ? { ...o, tree: false } : o));
  }, [narrowTree]);

  const editor = useRef(null);
  const preview = useRef(null);
  const attach = useRef(null);
  // What is on disk: content with \n line ends, the etag, and the note's own line end.
  const saved = useRef({ content: "", etag: null, path: null, eol: "\n" });
  const saveTimer = useRef(null);
  const saveQueue = useRef(Promise.resolve());
  const latest = useRef({ path, content });
  latest.current = { path, content };
  const statusRef = useRef(status);
  statusRef.current = status;
  const loadSeq = useRef(0);
  const handled = useRef(null);

  const isText = !!path && TEXT_EXT.test(path);
  const showAi = inspectorShown && tab === "ai";
  useEffect(() => {
    if (showAi) setAiMounted(true);
  }, [showAi]);

  // ------------------------------------------------------------- saving
  /** Writes the open note. Resolves to false when it could not be saved (error or conflict). */
  const doSave = useCallback(async (force, { keepalive = false } = {}) => {
    clearTimeout(saveTimer.current);
    const { path: current, content: text } = latest.current;
    if (!current || !TEXT_EXT.test(current) || saved.current.path !== current) return true;
    if (text === saved.current.content && !force) {
      setStatus((s) => (s === "conflict" ? s : "saved"));
      return true;
    }
    setStatus("saving");
    const { eol } = saved.current;
    try {
      const body = eol === "\r\n" ? text.replace(/\r?\n/g, "\r\n") : text;
      const result = await api.save(current, body, force ? null : saved.current.etag, { keepalive });
      if (saved.current.path === current) saved.current = { path: current, content: text, etag: result.etag, eol };
      setStatus(latest.current.content === text ? "saved" : "dirty");
      ctx.events.emit("note:saved", { path: current });
      return true;
    } catch (err) {
      const conflict = err instanceof ApiError && err.status === 409 && err.code !== "no_vault";
      setStatus(conflict ? "conflict" : "error");
      if (!conflict) toast.err(`保存失败：${err.message}`);
      return false;
    }
  }, [ctx.events]);

  /**
   * Saves now (queued behind a save in flight, so etags stay in order).
   * Resolves to false when the note could not be saved.
   */
  const save = useCallback(
    (force = false, options) => {
      saveQueue.current = saveQueue.current.then(() => doSave(force, options));
      return saveQueue.current;
    },
    [doSave],
  );

  const onEdit = useCallback(
    (text) => {
      latest.current = { ...latest.current, content: text };
      setContent(text);
      clearTimeout(saveTimer.current);
      if (text === saved.current.content) {
        setStatus((s) => (s === "conflict" ? s : "saved"));
        return;
      }
      setStatus((s) => (s === "conflict" ? s : "dirty"));
      saveTimer.current = setTimeout(() => statusRef.current !== "conflict" && save(), 1200);
    },
    [save],
  );

  // The page may go away at any moment (a reload, a closing window, the app
  // hidden): write the edit now rather than when the 1.2 s timer would have.
  useEffect(() => {
    const unsaved = () => !!saved.current.path && latest.current.content !== saved.current.content;
    const flush = () => {
      if (unsaved() && statusRef.current !== "conflict") save(false, { keepalive: true });
    };
    const beforeUnload = (event) => {
      if (!unsaved()) return;
      flush();
      event.preventDefault();
      event.returnValue = "";
    };
    const onVisibility = () => document.visibilityState === "hidden" && flush();
    window.addEventListener("beforeunload", beforeUnload);
    window.addEventListener("pagehide", flush);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.removeEventListener("beforeunload", beforeUnload);
      window.removeEventListener("pagehide", flush);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [save]);

  const { registerFlusher } = ctx;
  useEffect(() => registerFlusher(() => save()), [registerFlusher, save]);
  useEffect(() => () => void save(), [save]);
  useEffect(() => {
    if (!active) save();
  }, [active, save]);

  // ------------------------------------------------------------- opening
  const scrollToAnchor = (anchor) =>
    setTimeout(() => {
      const heading = [...document.querySelectorAll(".np-preview .markdown h1, .np-preview .markdown h2, .np-preview .markdown h3, .np-preview .markdown h4")].find(
        (h) => h.textContent.trim() === anchor.trim(),
      );
      heading?.scrollIntoView({ block: "start" });
    }, 60);

  const load = useCallback(
    async (target, hints) => {
      const seq = ++loadSeq.current;
      const leaving = latest.current.path;
      if (statusRef.current === "conflict" && leaving && leaving !== target && latest.current.content !== saved.current.content) {
        const discard = await confirm({
          title: "这篇笔记还有冲突没处理",
          message: "离开会丢掉你在这里的修改。可以先回去选「保留我的版本」或「对比差异」。",
          confirmLabel: "丢弃修改并离开",
          cancelLabel: "回去处理",
          danger: true,
        });
        if (seq !== loadSeq.current) return;
        if (!discard) {
          navigate({ view: "notes", path: leaving }, { replace: true });
          return;
        }
        saved.current = { ...saved.current, content: latest.current.content };
      }
      const ok = await save();
      if (seq !== loadSeq.current) return;
      // The save just failed (no permission, disk full, server gone): the edit exists only here.
      if (!ok && leaving && leaving !== target && latest.current.path === leaving && latest.current.content !== saved.current.content) {
        const discard = await confirm({
          title: "这篇笔记没能保存",
          message: "离开会丢掉还没保存的修改。",
          confirmLabel: "丢弃修改并离开",
          cancelLabel: "留下",
          danger: true,
        });
        if (seq !== loadSeq.current) return;
        if (!discard) {
          navigate({ view: "notes", path: leaving }, { replace: true });
          return;
        }
        saved.current = { ...saved.current, content: latest.current.content };
      }
      setPresenting(false);
      setPath(target);
      if (!target || !TEXT_EXT.test(target)) {
        saved.current = { path: target, content: "", etag: null, eol: "\n" };
        setDocKey(seq);
        setContent("");
        setStatus("saved");
        return;
      }
      setLoading(true);
      try {
        const note = await api.note(target);
        if (seq !== loadSeq.current) return;
        const text = toLF(note.content);
        saved.current = { path: target, content: text, etag: note.etag, eol: eolOf(note.content) };
        setDocKey(seq);
        setContent(text);
        setStatus("saved");
        if (hints?.mode) setMode(hints.mode);
        if (hints?.anchor) scrollToAnchor(hints.anchor);
        // A note opened to be written (a new one): the caret goes to its end.
        if (hints?.mode === "edit") setTimeout(() => editor.current?.gotoLine(Number.MAX_SAFE_INTEGER), 80);
      } catch (err) {
        if (seq !== loadSeq.current) return;
        toast.err(err.status === 404 ? `找不到 ${target}` : err.message);
        setPath(null);
        navigate("#!/notes", { replace: true });
      } finally {
        if (seq === loadSeq.current) setLoading(false);
      }
    },
    [save, setMode],
  );

  // Follow the route while this page is showing.
  useEffect(() => {
    if (!active || route.view !== "notes") return;
    const target = route.path ?? null;
    const key = `${target}\u0000${route.state ? JSON.stringify(route.state) : ""}`;
    if (handled.current === key) return;
    handled.current = key;
    if (target === latest.current.path && target !== null) {
      if (route.state?.mode) setMode(route.state.mode);
      if (route.state?.anchor) scrollToAnchor(route.state.anchor);
      return;
    }
    if (narrowTree) setOverlay((o) => ({ ...o, tree: false }));
    load(target, route.state);
  }, [active, route, load, setMode, narrowTree]);

  const reloadFromDisk = useCallback(async () => {
    const current = latest.current.path;
    if (!current || !TEXT_EXT.test(current)) return;
    const note = await api.note(current);
    if (latest.current.path !== current) return;
    const text = toLF(note.content);
    saved.current = { path: current, content: text, etag: note.etag, eol: eolOf(note.content) };
    setContent(text);
    setStatus("saved");
  }, []);

  // Files changed under us (sync, organize, a move that rewrote links).
  useEffect(
    () =>
      ctx.events.on("vault:changed", ({ paths = [] }) => {
        setRefreshKey((k) => k + 1);
        const current = latest.current.path;
        if (!current || !paths.includes(current)) return;
        if (latest.current.content === saved.current.content) reloadFromDisk().catch(() => {});
      }),
    [ctx.events, reloadFromDisk],
  );

  // Requests from other pages: show the inspector on a tab.
  useEffect(() => {
    const request = ctx.inspectorRequest;
    if (!request) return;
    setTab(request.tab || "ai");
    if (narrowInspector) setOverlay((o) => ({ ...o, inspector: true }));
    else setInspectorOpen(true);
  }, [ctx.inspectorRequest]); // eslint-disable-line react-hooks/exhaustive-deps

  // ⌘S saves in every mode (the editor handles it itself while focused).
  const onKeyDown = useEvent((event) => {
    if (!active) return;
    if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === "s") {
      event.preventDefault();
      save();
    }
  });
  useEffect(() => {
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onKeyDown]);

  // ------------------------------------------------------------- actions
  const openPath = (target) => ctx.openNote(target);

  /** A click on a [[missing link]]: create the note it names (where its path says; Inbox/ for a bare name). */
  const createFromLink = async (name) => {
    const target = String(name || "").trim().replace(/^\/+/, "").replace(/\.md$/i, "");
    if (!target) return;
    const chosen = await prompt({
      title: "新建笔记",
      icon: "file-plus",
      message: `「${target.split("/").pop()}」还不存在。`,
      label: "路径",
      hint: target.includes("/") ? "放在链接写的位置，链接就能直接找到它" : "之后可以再移动到合适的位置，链接会跟着更新",
      defaultValue: target.includes("/") ? target : `Inbox/${target}`,
      selectName: true,
      confirmLabel: "创建",
    });
    if (!chosen) return;
    const clean = chosen.replace(/^\/+/, "");
    const notePath = /\.md$/i.test(clean) ? clean : `${clean}.md`;
    try {
      await api.create(notePath, `# ${noteTitle(notePath)}\n\n`);
      await ctx.reloadTree();
      ctx.openNote(notePath, { mode: "edit" });
    } catch (err) {
      toast.err(err.message);
    }
  };

  const getSelection = () => {
    if (mode !== "read" && editor.current) return editor.current.getSelection();
    return window.getSelection()?.toString() || "";
  };

  const applyAI = async ({ type, text, original, title, present }) => {
    const clean = text.trim().replace(/^```(?:markdown|md)?\n([\s\S]*)\n```$/, "$1");
    const current = latest.current.path;
    if (type === "newNote") {
      const base = present && current ? `${current.replace(/\.md$/i, "")}（幻灯片）` : `Inbox/${(title || "AI 笔记").replace(/[\\/:*?"<>|#^[\]]/g, " ").trim()}`;
      const name = await prompt({
        title: present ? "保存幻灯片为新笔记" : "保存为新笔记",
        icon: "file-plus",
        label: "路径",
        hint: present ? undefined : "之后可以用「整理知识库」把它归档",
        defaultValue: base,
        confirmLabel: "保存",
      });
      if (!name) return;
      const notePath = /\.md$/i.test(name) ? name : `${name}.md`;
      try {
        await api.create(notePath, clean);
        await ctx.reloadTree();
        ctx.openNote(notePath);
        if (present) setTimeout(() => setPresenting(true), 250);
      } catch (err) {
        toast.err(err.message);
      }
      return;
    }
    if (!current || !TEXT_EXT.test(current)) return toast.warn("请先打开一篇笔记");
    const now = latest.current.content;
    if (type === "replaceText") {
      const index = now.indexOf(original);
      if (index < 0) return toast.warn("原文已变化，或是在阅读模式里选中的（与源码不一致）。请在编辑模式下重新选择。");
      onEdit(now.slice(0, index) + clean + now.slice(index + original.length));
      toast.ok("已替换");
    } else if (type === "replaceAll") {
      setConfirmDiff({ before: now, after: clean.endsWith("\n") ? clean : `${clean}\n` });
    } else if (type === "append") {
      onEdit(`${now.replace(/\s*$/, "")}\n\n${clean}\n`);
      toast.ok("已追加到笔记末尾");
    } else if (type === "insert") {
      if (mode !== "read" && editor.current) editor.current.insertAtCursor(clean);
      else {
        onEdit(`${now.replace(/\s*$/, "")}\n\n${clean}\n`);
        toast.ok("已插入到笔记末尾");
      }
    }
  };

  const insertAttachments = async (list) => {
    const stored = await ctx.uploadFiles(list, me.assets_dir || undefined);
    if (!stored.length) return;
    const text = stored.map((p) => `![[${p}]]`).join("\n") + "\n";
    if (mode !== "read" && editor.current) editor.current.insertAtCursor(text);
    else onEdit(`${latest.current.content.replace(/\s*$/, "")}\n\n${text}`);
  };

  const showConflictDiff = async () => {
    try {
      const disk = await api.note(path);
      setConflictDiff({ disk: toLF(disk.content) });
    } catch (err) {
      toast.err(err.message);
    }
  };

  const resolveConflict = async (keepMine) => {
    setConflictDiff(null);
    if (keepMine) await save(true);
    else await reloadFromDisk().catch((err) => toast.err(err.message));
  };

  const switchMode = (next) => {
    save();
    setMode(next);
  };

  const toggleInspector = () => {
    if (narrowInspector) setOverlay((o) => ({ tree: false, inspector: !o.inspector }));
    else setInspectorOpen(!inspectorOpen);
  };
  const closeInspector = () => (narrowInspector ? setOverlay((o) => ({ ...o, inspector: false })) : setInspectorOpen(false));

  const docMenu = () => {
    if (!path) return [];
    const title = noteTitle(path);
    return [
      isText && { label: "复制双链", icon: "link", hint: `[[${title}]]`, onSelect: () => copyText(`[[${title}]]`).then(() => toast.ok("已复制双链")) },
      { label: "复制路径", icon: "copy", onSelect: () => copyText(path).then(() => toast.ok("已复制路径")) },
      isText && { label: "插入附件…", icon: "upload", hint: me.assets_dir ? `存到 ${me.assets_dir}/` : undefined, onSelect: () => attach.current?.click() },
      ctx.host.native && { label: "在 Finder 中显示", icon: "folder-open", onSelect: () => ctx.revealPath(path) },
      !ctx.host.native && !isText && { label: "下载", icon: "download", onSelect: () => window.open(`${fileUrl(path)}&download=true`, "_blank", "noopener") },
      { divider: true },
      { label: "重命名 / 移动…", icon: "edit", onSelect: () => ctx.renamePath(path) },
      isText && { label: "历史版本", icon: "history", onSelect: () => ctx.openInspector("history") },
      isText && { label: "演示", icon: "present", onSelect: () => (save(), setPresenting(true)) },
      { divider: true },
      { label: isText ? "删除笔记" : "删除文件", icon: "trash", danger: true, onSelect: () => ctx.deletePath(path) },
    ].filter(Boolean);
  };

  // ------------------------------------------------------------- render
  const hasIndex = files.includes("index.md");

  const doc = (() => {
    if (!path) {
      return (
        <div className="np-empty">
          <EmptyState
            icon="file-text"
            title={loading ? "正在打开…" : "从左边选一篇笔记"}
            actions={
              <>
                <Button icon="file-plus" onClick={() => ctx.newNote("")}>新建笔记</Button>
                {hasIndex && <Button icon="graph" variant="ghost" onClick={() => ctx.openNote("index.md")}>知识地图</Button>}
                <Button icon="search" variant="ghost" onClick={() => ctx.focusSearch("")}>搜索</Button>
              </>
            }
          >
            支持 [[双链]]、公式、Mermaid 图表与 Obsidian 的写法。
          </EmptyState>
        </div>
      );
    }
    if (!isText) {
      const url = fileUrl(path);
      return (
        <div className="np-file">
          {IMAGE_EXT.test(path) ? (
            <img src={url} alt={path} />
          ) : /\.pdf$/i.test(path) ? (
            <iframe className="np-pdf" src={url} title={path} />
          ) : (
            <EmptyState icon="file" title="无法预览这个文件" actions={ctx.host.native ? <Button icon="folder-open" onClick={() => ctx.revealPath(path)}>在 Finder 中显示</Button> : <Button icon="download" onClick={() => window.open(`${url}&download=true`, "_blank", "noopener")}>下载</Button>}>
              {path}
            </EmptyState>
          )}
        </div>
      );
    }
    return (
      <div className={`np-body mode-${mode} ${loading ? "is-loading" : ""}`.trim()}>
        {(mode === "edit" || mode === "split") && (
          <div className="np-editor">
            <Editor ref={editor} value={content} docKey={docKey} onChange={onEdit} onSave={() => save()} assetsDir={me.assets_dir} />
          </div>
        )}
        {(mode === "read" || mode === "split") && (
          <div className="np-preview preview-scroll">
            <Preview ref={preview} text={content} path={path} resolver={resolver} onNavigate={(p, anchor) => ctx.openNote(p, { anchor })} onCreate={createFromLink} />
          </div>
        )}
      </div>
    );
  })();

  return (
    <div ref={root} className={`np ${inspectorShown ? "insp-on" : ""} ${treeShown ? "tree-on" : ""} ${narrowInspector ? "narrow-insp" : ""} ${narrowTree ? "narrow-tree" : ""}`.replace(/\s+/g, " ").trim()}>
      {active && (
        <TopbarActions>
          {narrowTree && <IconButton icon="folder" label="目录" active={overlay.tree} onClick={() => setOverlay((o) => ({ inspector: false, tree: !o.tree }))} />}
          {isText && <SaveStatus status={status} onRetry={() => save()} />}
          {isText && <Segmented size="sm" value={mode} onChange={switchMode} options={MODES} ariaLabel="视图" className="np-modes" />}
          {isText && <IconButton icon="present" label="演示（按 --- 或标题分页）" onClick={() => (save(), setPresenting(true))} />}
          {path && <MenuButton items={docMenu} label="更多操作" />}
          <IconButton icon="panel-right" label={inspectorShown ? "隐藏侧栏" : "显示侧栏（AI · 大纲 · 反链 · 历史）"} active={inspectorShown} onClick={toggleInspector} />
        </TopbarActions>
      )}

      <aside className="np-tree" aria-label="笔记目录">
        <FileTree current={path} onOpen={openPath} />
      </aside>

      <section className="np-doc" aria-label={path ? noteTitle(path) : "笔记"}>
        {status === "conflict" && (
          <div className="np-conflict" role="alert">
            <Icon name="alert" size={15} />
            <span className="grow">这篇笔记在别处（比如 Obsidian）被改过了。</span>
            <Button size="sm" variant="ghost" onClick={showConflictDiff}>对比差异</Button>
            <Button size="sm" onClick={() => resolveConflict(false)}>载入磁盘版本</Button>
            <Button size="sm" variant="primary" onClick={() => resolveConflict(true)}>保留我的版本</Button>
          </div>
        )}
        {doc}
      </section>

      <aside className="np-insp" aria-label="侧栏" aria-hidden={!inspectorShown || undefined}>
        <div className="np-insp-head">
          <Segmented size="sm" fill value={tab} onChange={setTab} options={TABS} ariaLabel="侧栏内容" />
          <IconButton icon="x" size="sm" label="关闭侧栏" onClick={closeInspector} />
        </div>
        <div className="np-insp-body">
          {aiMounted && (
            <div className="np-ai" hidden={tab !== "ai"}>
              <AIPanel
                config={me}
                embedded
                path={isText ? path : null}
                noteContent={isText ? content : ""}
                getSelection={getSelection}
                onApply={applyAI}
                onOpenNote={openPath}
                onDistill={(chatPath) => ctx.startOrganize({ preset: "inbox", chats: [chatPath], task: DISTILL_TASK })}
                onChatsChanged={ctx.reloadInbox}
                initialChat={ctx.pendingChat?.path ?? null}
                onInitialChatConsumed={ctx.clearPendingChat}
              />
            </div>
          )}
          {tab !== "ai" && (
            <div className="np-info info-panel">
              <InfoPanel
                tab={tab}
                path={isText ? path : null}
                content={content}
                refreshKey={refreshKey + (status === "saved" ? 1 : 0)}
                onOpen={openPath}
                onHeading={(item) => (mode === "read" ? preview.current?.scrollToHeading(item.id) : editor.current?.gotoLine(item.line))}
                onRestore={(text) => setConfirmDiff({ before: latest.current.content, after: toLF(text) })}
              />
            </div>
          )}
        </div>
      </aside>

      {((narrowTree && overlay.tree) || (narrowInspector && overlay.inspector)) && <div className="np-scrim" onClick={() => setOverlay({ tree: false, inspector: false })} />}

      <input
        ref={attach}
        type="file"
        multiple
        hidden
        onChange={(event) => {
          if (event.target.files?.length) insertAttachments(event.target.files);
          event.target.value = "";
        }}
      />

      <Dialog
        open={!!confirmDiff}
        onClose={() => setConfirmDiff(null)}
        title="确认替换全文"
        icon="columns"
        width={760}
        footerNote="应用后会自动保存；随时可以在「历史」里恢复旧版本。"
        footer={
          <>
            <Button onClick={() => setConfirmDiff(null)}>取消</Button>
            <Button
              variant="primary"
              onClick={() => {
                onEdit(confirmDiff.after);
                setConfirmDiff(null);
              }}
            >
              应用
            </Button>
          </>
        }
      >
        {confirmDiff && <DiffView before={confirmDiff.before} after={confirmDiff.after} />}
      </Dialog>

      <Dialog
        open={!!conflictDiff}
        onClose={() => setConflictDiff(null)}
        title="磁盘版本与你的修改"
        icon="alert"
        width={760}
        footerNote="− 只在磁盘版本里，+ 只在你的版本里"
        footer={
          <>
            <Button onClick={() => resolveConflict(false)}>载入磁盘版本</Button>
            <Button variant="primary" onClick={() => resolveConflict(true)}>保留我的版本</Button>
          </>
        }
      >
        {conflictDiff && <DiffView before={conflictDiff.disk} after={content} />}
      </Dialog>

      {presenting && isText && (
        <Slides
          content={content}
          path={path}
          resolver={resolver}
          onClose={() => {
            if (document.fullscreenElement) document.exitFullscreen();
            setPresenting(false);
          }}
        />
      )}
    </div>
  );
}

