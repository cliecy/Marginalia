import { createContext, useContext } from "react";
import { createPortal } from "react-dom";
import { useApp } from "../app/AppContext.jsx";
import { IconButton } from "../ui/Button.jsx";
import { findNav, subsFor } from "./nav.js";

/** The DOM node of the top bar's right-hand action area (set by the shell). */
export const TopbarSlotContext = createContext(null);

/**
 * Renders its children into the top bar's action area, at the right — the
 * place for a page's own tools (small pills, icon buttons, an accent-soft
 * primary action). Render it only while the page is the one showing.
 */
export function TopbarActions({ children }) {
  const slot = useContext(TopbarSlotContext);
  return slot ? createPortal(children, slot) : null;
}

/** The breadcrumb trail for a route: [{label, onClick?}]; the last segment is the page. */
export function useCrumbs() {
  const ctx = useApp();
  const { route } = ctx;
  if (route.view === "notes") {
    const crumbs = [{ label: "笔记", onClick: route.path ? () => ctx.revealInTree("") : undefined }];
    if (route.path) {
      const parts = route.path.split("/");
      parts.forEach((part, index) => {
        const last = index === parts.length - 1;
        const folder = parts.slice(0, index + 1).join("/");
        crumbs.push({ label: last ? part.replace(/\.md$/i, "") : part, onClick: last ? undefined : () => ctx.revealInTree(folder), title: last ? route.path : folder });
      });
    }
    return crumbs;
  }
  const { group, item } = findNav(route.view);
  if (!item) return [{ label: "Marginalia" }];
  const crumbs = [];
  if (group) crumbs.push({ label: group.label });
  const subs = subsFor(route.view, ctx);
  const sub = subs.find((s) => s.id === route.sub) || (subs.length ? subs[0] : null);
  crumbs.push({ label: item.label, onClick: sub ? () => ctx.go(route.view) : undefined });
  if (sub) crumbs.push({ label: sub.label });
  return crumbs;
}

/**
 * The 46px bar over the content: the sidebar toggle, where the reader is,
 * and the page's actions. In the desktop app it drags the window and keeps
 * clear of the traffic lights while the sidebar is hidden.
 */
export default function Topbar({ slotRef, sidebarShown, onToggleSidebar }) {
  const crumbs = useCrumbs();
  return (
    <header className="topbar">
      <IconButton icon="sidebar" label={sidebarShown ? "隐藏侧边栏" : "显示侧边栏"} className={`side-toggle ${sidebarShown ? "shown" : ""}`.trim()} onClick={onToggleSidebar} />
      <nav className="crumbs" aria-label="位置">
        {crumbs.map((crumb, index) => {
          const last = index === crumbs.length - 1;
          return (
            <span key={index} className="crumb-part">
              {index > 0 && <i aria-hidden="true">/</i>}
              {last ? (
                <b title={crumb.title}>{crumb.label}</b>
              ) : crumb.onClick ? (
                <button type="button" onClick={crumb.onClick} title={crumb.title}>
                  {crumb.label}
                </button>
              ) : (
                <span title={crumb.title}>{crumb.label}</span>
              )}
            </span>
          );
        })}
      </nav>
      <div className="topbar-actions" ref={slotRef} />
    </header>
  );
}
