import { useCallback, useEffect, useState } from "react";
import { AppProvider, useApp } from "./app/AppContext.jsx";
import { useMediaQuery } from "./app/hooks.js";
import { useRoute } from "./app/router.js";
import { api, ApiError } from "./lib/api.js";
import Sidebar from "./shell/Sidebar.jsx";
import Topbar, { TopbarSlotContext } from "./shell/Topbar.jsx";
import Button from "./ui/Button.jsx";
import { DialogHost } from "./ui/Dialog.jsx";
import { Logo } from "./ui/Icon.jsx";
import { MenuHost } from "./ui/Popover.jsx";
import Spinner from "./ui/Spinner.jsx";
import { ToastHost } from "./ui/Toast.jsx";
import NotePage from "./pages/NotePage.jsx";
import Home from "./pages/Home.jsx";
import Chats from "./pages/Chats.jsx";
import Graph from "./pages/Graph.jsx";
import Organize from "./pages/Organize.jsx";
import Prompts from "./pages/Prompts.jsx";
import Sync from "./pages/Sync.jsx";
import Api from "./pages/Api.jsx";
import Magpie from "./pages/Magpie.jsx";
import Settings from "./pages/Settings.jsx";
import Onboarding from "./pages/Onboarding.jsx";
import Login from "./pages/Login.jsx";

/** View id → page component (every page gets { route, sub }). */
const PAGES = {
  home: Home,
  chats: Chats,
  graph: Graph,
  organize: Organize,
  prompts: Prompts,
  sync: Sync,
  api: Api,
  magpie: Magpie,
  settings: Settings,
};

/** The page for the current view; the notes workspace stays mounted once visited. */
function Content() {
  const ctx = useApp();
  const { route } = ctx;
  const [notesVisited, setNotesVisited] = useState(route.view === "notes");
  useEffect(() => {
    if (route.view === "notes") setNotesVisited(true);
  }, [route.view]);

  const Page = PAGES[route.view];
  let page = null;
  if (Page) {
    const extra =
      route.view === "organize"
        ? { request: ctx.organizeRequest, onRequestConsumed: ctx.clearOrganizeRequest }
        : route.view === "settings"
          ? { sub: route.sub || "general" }
          : {};
    page = (
      <div key={route.view} className="view">
        <Page route={route} sub={route.sub} {...extra} />
      </div>
    );
  }

  return (
    <>
      {notesVisited && (
        <div className="view view-notes" hidden={route.view !== "notes"}>
          <NotePage active={route.view === "notes"} />
        </div>
      )}
      {page}
    </>
  );
}

/** Sidebar | (top bar over the page). */
function Shell() {
  const ctx = useApp();
  const [slot, setSlot] = useState(null);
  const compact = useMediaQuery("(max-width: 720px)");
  const [drawer, setDrawer] = useState(false);
  const route = ctx.route;

  useEffect(() => {
    if (!compact) setDrawer(false);
  }, [compact]);
  useEffect(() => {
    setDrawer(false);
  }, [route.view, route.path, route.sub]);

  const sidebarShown = compact ? drawer : !ctx.sidebarCollapsed;
  const toggle = () => (compact ? setDrawer(!drawer) : ctx.toggleSidebar());

  return (
    <div className={`shell ${sidebarShown ? "" : "side-collapsed"} ${compact ? "compact" : ""} ${ctx.host.native ? "native" : ""}`.replace(/\s+/g, " ").trim()}>
      <Sidebar onNavigate={() => setDrawer(false)} />
      {compact && drawer && <div className="side-scrim" onClick={() => setDrawer(false)} />}
      <div className="main">
        <Topbar slotRef={setSlot} sidebarShown={sidebarShown} onToggleSidebar={toggle} />
        <TopbarSlotContext.Provider value={slot}>
          <div className="content">
            <Content />
          </div>
        </TopbarSlotContext.Provider>
      </div>
    </div>
  );
}

function Splash({ error, onRetry }) {
  return (
    <div className="splash">
      <div className="splash-inner">
        <Logo size={36} />
        {error ? (
          <>
            <p className="splash-error">{error}</p>
            <Button icon="refresh" onClick={onRetry}>重试</Button>
          </>
        ) : (
          <Spinner size={16} />
        )}
      </div>
    </div>
  );
}

/**
 * Boot: who is the user, is a vault open? Then the shell.
 *   /api/auth/me 401        → Login (browser / server mode)
 *   vault.configured false  → Onboarding (desktop first run)
 *   otherwise               → the app
 */
export default function App() {
  const [me, setMe] = useState(undefined);
  const [bootError, setBootError] = useState("");
  useRoute(); // keep the shell in step with the hash

  const loadMe = useCallback(async () => {
    setBootError("");
    try {
      setMe(await api.me());
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) setMe(null);
      else setBootError(err.message || "无法连接到服务");
    }
  }, []);

  useEffect(() => {
    loadMe();
    const onAuth = () => setMe(null);
    const onVault = () => loadMe();
    window.addEventListener("auth-required", onAuth);
    window.addEventListener("vault-required", onVault);
    return () => {
      window.removeEventListener("auth-required", onAuth);
      window.removeEventListener("vault-required", onVault);
    };
  }, [loadMe]);

  let body;
  if (bootError) body = <Splash error={bootError} onRetry={loadMe} />;
  else if (me === undefined) body = <Splash />;
  else if (me === null) body = <Login onDone={loadMe} />;
  else if (me.vault && me.vault.configured === false) body = <Onboarding me={me} onDone={loadMe} />;
  else
    body = (
      <AppProvider me={me} reloadMe={loadMe}>
        <Shell />
      </AppProvider>
    );

  return (
    <>
      {body}
      <MenuHost />
      <DialogHost />
      <ToastHost />
    </>
  );
}
