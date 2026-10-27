import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../lib/api.js";
import { LinkResolver } from "../lib/markdown.js";
import { isDesktop, onFlushRequest, onMenu, onOpenLink, reveal } from "../lib/native.js";
import { confirm, prompt } from "../ui/Dialog.jsx";
import { toast } from "../ui/Toast.jsx";
import { createEmitter, useEvent, useStoredState } from "./hooks.js";
import { applyTheme, storedTheme } from "./theme.js";
import { navigate, useRoute } from "./router.js";
import { noteTitle } from "./format.js";

/**
 * AppContext — everything the shell knows and can do, shared with every page.
 * See scratchpad/frontend-contract.md for the full field list.
 */
const AppContext = createContext(null);

/** The app context. Throws outside <AppProvider>. */
export function useApp() {
  const ctx = useContext(AppContext);
  if (!ctx) throw new Error("useApp() used outside <AppProvider>");
  return ctx;
}

/** Flattens the tree to file paths in tree order. */
function flatten(nodes, out = []) {
  for (const node of nodes) {
    if (node.type === "folder") flatten(node.children || [], out);
    else out.push(node.path);
  }
  return out;
}

/** Visits every node of the tree. */
export function walkTree(nodes, visit, parents = []) {
  for (const node of nodes) {
    visit(node, parents);
    if (node.type === "folder") walkTree(node.children || [], visit, [...parents, node]);
  }
}

const within = (path, target) => path === target || path?.startsWith(`${target}/`);

/**
 * Holds the app's shared state once the user is signed in and a vault is open.
 *
 * @param {object} props
 * @param {object} props.me        /api/auth/me
 * @param {() => Promise<void>} props.reloadMe
 */
export function AppProvider({ me, reloadMe, children }) {
  const route = useRoute();
  const desktop = !!me.desktop || (isDesktop && me.mode === "desktop");
  const host = useMemo(
    () => ({
      desktop,
      native: isDesktop,
      mode: me.mode,
      version: me.version || "",
    }),
    [desktop, me.mode, me.version],
  );

  // ------------------------------------------------------------ vault data
  const [tree, setTree] = useState([]);
  const [treeLoaded, setTreeLoaded] = useState(false);
  const [inbox, setInbox] = useState(0);
  const [chats, setChats] = useState(null);
  const [git, setGit] = useState(null);
  const [syncing, setSyncing] = useState(false);
  const [settings, setSettings] = useState(null);
  const [integrations, setIntegrations] = useState(null);
  const [theme, setThemeState] = useState(storedTheme);
  const [sidebarCollapsed, setSidebarCollapsed] = useStoredState("mn.sidebar.collapsed", false);
  const [lastNote, setLastNote] = useStoredState("mn.lastNote", null);

  const files = useMemo(() => flatten(tree), [tree]);
  const resolver = useMemo(() => new LinkResolver(files), [files]);
  const emitter = useMemo(() => createEmitter(), []);

  const reloadTree = useCallback(
    () =>
      api
        .tree()
        .then((r) => {
          setTree(r.tree || []);
          setTreeLoaded(true);
        })
        .catch((err) => {
          setTreeLoaded(true);
          if (err.status !== 401 && err.code !== "no_vault") toast.err(`读取笔记目录失败：${err.message}`);
        }),
    [],
  );
  const reloadInbox = useCallback(
    () =>
      api
        .chats()
        .then((r) => {
          setChats(r.chats || []);
          setInbox((r.chats || []).filter((c) => c.status === "inbox").length);
        })
        .catch(() => {}),
    [],
  );
  const reloadGit = useCallback(() => api.gitStatus().then(setGit).catch(() => setGit(null)), []);
  const reloadSettings = useCallback(() => {
    return api
      .appSettings()
      .then((s) => {
        setSettings(s);
        return s;
      })
      .catch(() => null);
  }, []);
  const reloadIntegrations = useCallback(() => {
    return api
      .integrations()
      .then((s) => {
        setIntegrations(s);
        return s;
      })
      .catch(() => null);
  }, []);

  /** PUT a partial settings patch; resolves to the new settings (and keeps them). */
  const updateSettings = useCallback(async (patch) => {
    const next = await api.updateAppSettings(patch);
    setSettings(next);
    return next;
  }, []);

  useEffect(() => {
    reloadTree();
    reloadInbox();
    reloadGit();
    reloadSettings().then((s) => {
      const chosen = s?.appearance?.theme;
      if (desktop && chosen) {
        applyTheme(chosen);
        setThemeState(chosen);
      }
    });
    reloadIntegrations();
  }, [reloadTree, reloadInbox, reloadGit, reloadSettings, reloadIntegrations, desktop]);

  // Keep the integrations (Magpie gateway, API listener) fresh while visible.
  useEffect(() => {
    const timer = setInterval(() => document.visibilityState === "visible" && reloadIntegrations(), 60000);
    return () => clearInterval(timer);
  }, [reloadIntegrations]);

  // Coming back to the window: pick up edits made elsewhere (Obsidian, git, agents).
  const lastFocus = useRef(Date.now());
  useEffect(() => {
    const onFocus = () => {
      if (Date.now() - lastFocus.current < 15000) return;
      lastFocus.current = Date.now();
      reloadTree();
      reloadGit();
      reloadInbox();
      reloadIntegrations();
    };
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [reloadTree, reloadGit, reloadInbox, reloadIntegrations]);

  // ------------------------------------------------------------ theme
  const setTheme = useCallback(
    (value) => {
      applyTheme(value, { animate: true });
      setThemeState(value);
      if (desktop) {
        api
          .updateAppSettings({ appearance: { theme: value } })
          .then(setSettings)
          .catch((err) => toast.err(`外观设置未能保存：${err.message}`));
      }
    },
    [desktop],
  );

  // ------------------------------------------------------------ editing bridge
  const flushers = useRef(new Set());
  /**
   * Lets the notes workspace save before the vault changes underneath it. `fn`
   * resolves to false when the save failed. Returns an unregister function.
   */
  const registerFlusher = useCallback((fn) => {
    flushers.current.add(fn);
    return () => flushers.current.delete(fn);
  }, []);
  /** Saves any unsaved edit of the open note. Resolves to false when an edit could not be saved (it is still unsaved). */
  const flushEdits = useCallback(async () => {
    const results = await Promise.all([...flushers.current].map((fn) => Promise.resolve(fn()).catch(() => false)));
    return results.every((ok) => ok !== false);
  }, []);

  /** Tells everyone files changed outside the editor; reloads the tree, inbox and git status. */
  const notifyVaultChanged = useCallback(
    (paths = []) => {
      reloadTree();
      reloadInbox();
      reloadGit();
      emitter.emit("vault:changed", { paths });
    },
    [emitter, reloadTree, reloadInbox, reloadGit],
  );

  // ------------------------------------------------------------ navigation
  useEffect(() => {
    if (route.view === "notes" && route.path) setLastNote(route.path);
  }, [route.view, route.path, setLastNote]);

  const go = useCallback(
    (view, sub = null, options = {}) => {
      if (view === "notes" && !sub) {
        const known = lastNote && (!treeLoaded || files.includes(lastNote));
        navigate(known ? { view: "notes", path: lastNote } : "#!/notes", options);
        return;
      }
      navigate({ view, sub }, options);
    },
    [lastNote, files, treeLoaded],
  );
  /** Opens a note. options: {anchor?, mode?: "read"|"edit"|"split", replace?} */
  const openNote = useCallback((path, { anchor = null, mode = null, replace = false } = {}) => {
    if (!path) return navigate("#!/notes", { replace });
    const state = anchor || mode ? { anchor, mode, at: Date.now() } : null;
    navigate({ view: "notes", path }, { replace, state });
  }, []);
  const goHome = useCallback(() => navigate("#/"), []);

  // ------------------------------------------------------------ cross-page requests
  const [organizeRequest, setOrganizeRequest] = useState(null);
  const [pendingChat, setPendingChat] = useState(null);
  const [searchRequest, setSearchRequest] = useState(null);
  const [treeReveal, setTreeReveal] = useState(null);
  const [inspectorRequest, setInspectorRequest] = useState(null);

  /** Opens the organize page with a request: {preset?, chats?: string[], task?}. */
  const startOrganize = useCallback((request = {}) => {
    setOrganizeRequest({ preset: request.preset ?? null, chats: request.chats ?? [], task: request.task ?? null, nonce: Date.now() });
    navigate("#!/organize");
  }, []);
  const clearOrganizeRequest = useCallback(() => setOrganizeRequest(null), []);

  /** Shows a saved chat (or a fresh one with null) in the notes workspace's AI panel. */
  const openChatInPanel = useCallback(
    (chatPath = null) => {
      setPendingChat(chatPath ? { path: chatPath, nonce: Date.now() } : null);
      setInspectorRequest({ tab: "ai", nonce: Date.now() });
      if (route.view !== "notes") go("notes");
    },
    [route.view, go],
  );
  const clearPendingChat = useCallback(() => setPendingChat(null), []);

  /** Opens the notes workspace's inspector on a tab: "ai" | "outline" | "backlinks" | "history". */
  const openInspector = useCallback(
    (tab = "ai") => {
      setInspectorRequest({ tab, nonce: Date.now() });
      if (route.view !== "notes") go("notes");
    },
    [route.view, go],
  );

  /** Focuses the file tree's search (optionally filling it). */
  const focusSearch = useCallback(
    (q = "") => {
      setSearchRequest({ q, nonce: Date.now() });
      if (route.view !== "notes") go("notes");
    },
    [route.view, go],
  );

  /** Expands the tree to a folder or file and scrolls it into view. */
  const revealInTree = useCallback(
    (path) => {
      setTreeReveal({ path, nonce: Date.now() });
      if (route.view !== "notes") go("notes");
    },
    [route.view, go],
  );

  // ------------------------------------------------------------ file operations
  const newNote = useCallback(
    async (folder = "") => {
      const name = await prompt({
        title: "新建笔记",
        icon: "file-plus",
        label: "名称",
        hint: "可以带上文件夹，比如 Knowledge/机器学习/PPO",
        defaultValue: folder ? `${folder}/` : "",
        placeholder: "Knowledge/机器学习/PPO",
        confirmLabel: "创建",
      });
      if (!name) return null;
      let notePath = name.replace(/^\/+/, "");
      if (!/\.md$/i.test(notePath)) notePath += ".md";
      try {
        await api.create(notePath, `# ${noteTitle(notePath)}\n\n`);
        await reloadTree();
        openNote(notePath, { mode: "edit" });
        return notePath;
      } catch (err) {
        toast.err(err.message);
        return null;
      }
    },
    [reloadTree, openNote],
  );

  const newFolder = useCallback(
    async (parent = "") => {
      const name = await prompt({
        title: "新建文件夹",
        icon: "folder-plus",
        label: "路径",
        defaultValue: parent ? `${parent}/` : "",
        placeholder: "Knowledge/机器学习",
        confirmLabel: "创建",
      });
      if (!name) return null;
      const path = name.replace(/^\/+|\/+$/g, "");
      try {
        await api.create(path, "", true);
        await reloadTree();
        setTreeReveal({ path, nonce: Date.now() });
        return path;
      } catch (err) {
        toast.err(err.message);
        return null;
      }
    },
    [reloadTree],
  );

  const renamePath = useCallback(
    async (path) => {
      const to = await prompt({
        title: "重命名 / 移动",
        icon: "edit",
        message: "其他笔记里指向它的双链会自动更新。",
        label: "新路径",
        defaultValue: path,
        selectName: true,
        mono: true,
        confirmLabel: "移动",
      });
      if (!to || to === path) return null;
      try {
        if (!(await flushEdits())) {
          toast.err("当前笔记没能保存，先处理保存失败再移动");
          return null;
        }
        const result = await api.move(path, to);
        const updated = result.updated_links_in || [];
        if (updated.length) toast.ok(`已移动，并更新了 ${updated.length} 篇笔记中的链接`);
        else toast.ok("已移动");
        if (route.view === "notes" && route.path && within(route.path, path)) {
          openNote(result.path + route.path.slice(path.length), { replace: true });
        }
        notifyVaultChanged([path, result.path, ...updated]);
        return result.path;
      } catch (err) {
        toast.err(err.message);
        return null;
      }
    },
    [flushEdits, notifyVaultChanged, openNote, route.view, route.path],
  );

  const deletePath = useCallback(
    async (path, { folder = false } = {}) => {
      const ok = await confirm({
        title: `删除「${noteTitle(path) || path}」？`,
        // Both servers delete only empty folders.
        message: folder ? "只能删除空文件夹。" : "之后可以从 git 历史中找回。",
        confirmLabel: "删除",
        danger: true,
      });
      if (!ok) return false;
      try {
        await api.remove(path);
        if (route.view === "notes" && route.path && within(route.path, path)) navigate("#!/notes", { replace: true });
        notifyVaultChanged([path]);
        toast.ok("已删除");
        return true;
      } catch (err) {
        toast.err(/only empty folders/i.test(err.message) ? "文件夹不是空的（可能含有隐藏文件），请先清空" : err.message);
        return false;
      }
    },
    [notifyVaultChanged, route.view, route.path],
  );

  /** Uploads files into a folder (default: the assets folder). Resolves to the stored paths. */
  const uploadFiles = useCallback(
    async (fileList, folder) => {
      const list = [...(fileList || [])];
      if (!list.length) return [];
      const stored = [];
      try {
        for (const file of list) {
          const result = await api.upload(file, folder || me.assets_dir || undefined);
          stored.push(result.path);
        }
        toast.ok(list.length === 1 ? `已上传 ${noteTitle(stored[0]) || stored[0]}` : `已上传 ${list.length} 个文件`);
      } catch (err) {
        toast.err(`上传失败：${err.message}`);
      }
      if (stored.length) notifyVaultChanged(stored);
      return stored;
    },
    [me.assets_dir, notifyVaultChanged],
  );

  /** Shows a vault path in Finder (desktop only). */
  const revealPath = useCallback(async (path = "") => {
    try {
      await reveal(path);
    } catch (err) {
      toast.err(err.message || String(err));
    }
  }, []);

  // ------------------------------------------------------------ git sync
  const syncingRef = useRef(false);
  const sync = useCallback(async () => {
    if (syncingRef.current) return null;
    syncingRef.current = true;
    setSyncing(true);
    try {
      if (!(await flushEdits())) {
        toast.err("当前笔记没能保存，先处理保存失败再同步");
        return null;
      }
      const result = await api.gitSync();
      toast.ok(result.pulled || result.pushed ? `已同步：拉取 ${result.pulled} 个提交，推送 ${result.pushed} 个提交` : "已是最新");
      if (result.changed?.length) notifyVaultChanged(result.changed);
      return result;
    } catch (err) {
      toast.err(`同步失败：${err.message}`);
      return null;
    } finally {
      syncingRef.current = false;
      setSyncing(false);
      reloadGit();
    }
  }, [flushEdits, notifyVaultChanged, reloadGit]);

  const logout = useCallback(async () => {
    if (!(await flushEdits())) {
      const leave = await confirm({
        title: "当前笔记没能保存",
        message: "退出登录会丢掉还没保存的修改。",
        confirmLabel: "仍然退出",
        cancelLabel: "留下",
        danger: true,
      });
      if (!leave) return;
    }
    try {
      await api.logout();
    } catch {}
    await reloadMe();
  }, [flushEdits, reloadMe]);

  const toggleSidebar = useCallback(() => setSidebarCollapsed((v) => !v), [setSidebarCollapsed]);

  // ------------------------------------------------------------ native menu, deep links, shortcuts
  const onMenuCommand = useEvent((action) => {
    if (action === "new-note") newNote(route.view === "notes" && route.path ? route.path.split("/").slice(0, -1).join("/") : "");
    else if (action === "open-vault") navigate("#!/settings/vault");
    else if (action === "settings") navigate("#!/settings");
    else if (action === "toggle-sidebar") toggleSidebar();
    else if (action === "reveal-vault") revealPath("");
    else if (action === "search") focusSearch("");
  });
  const onDeepLink = useEvent((request) => {
    if (request?.path) openNote(request.path);
    else if (request?.q != null) focusSearch(request.q);
  });
  useEffect(() => onMenu(onMenuCommand), [onMenuCommand]);
  useEffect(() => onOpenLink(onDeepLink), [onDeepLink]);
  // The window is about to close or the app to quit: write what is still unsaved.
  useEffect(() => onFlushRequest(flushEdits), [flushEdits]);

  const onKeyDown = useEvent((event) => {
    const mod = event.metaKey || event.ctrlKey;
    if (!mod || event.altKey) return;
    const key = event.key.toLowerCase();
    if (key === "k" && !event.shiftKey) {
      event.preventDefault();
      focusSearch("");
    } else if (!isDesktop && key === "\\") {
      // In the desktop app the View menu owns ⌘\.
      event.preventDefault();
      toggleSidebar();
    } else if (!isDesktop && key === "," && !event.shiftKey) {
      event.preventDefault();
      navigate("#!/settings");
    }
  });
  useEffect(() => {
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onKeyDown]);

  // ------------------------------------------------------------ the value
  const value = {
    me,
    host,
    reloadMe,
    route,
    navigate,
    go,
    goHome,
    openNote,
    lastNote,

    tree,
    treeLoaded,
    files,
    resolver,
    reloadTree,
    chats,
    inbox,
    reloadInbox,
    git,
    reloadGit,
    syncing,
    sync,
    settings,
    reloadSettings,
    updateSettings,
    integrations,
    reloadIntegrations,
    theme,
    setTheme,
    logout,

    sidebarCollapsed,
    setSidebarCollapsed,
    toggleSidebar,

    events: emitter,
    notifyVaultChanged,
    registerFlusher,
    flushEdits,

    newNote,
    newFolder,
    renamePath,
    deletePath,
    uploadFiles,
    revealPath,

    organizeRequest,
    startOrganize,
    clearOrganizeRequest,
    pendingChat,
    openChatInPanel,
    clearPendingChat,
    inspectorRequest,
    openInspector,
    searchRequest,
    focusSearch,
    treeReveal,
    revealInTree,
  };

  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}

export default AppContext;
