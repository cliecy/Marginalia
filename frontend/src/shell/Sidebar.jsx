import { useLayoutEffect, useRef, useState } from "react";
import { useApp } from "../app/AppContext.jsx";
import Icon, { Logo } from "../ui/Icon.jsx";
import StatusDot from "../ui/StatusDot.jsx";
import { NAV_GROUPS, SETTINGS_NAV, subsFor } from "./nav.js";

/** One page in the sidebar: icon, label, and a count or a dot at the right. */
function NavItem({ item, active, ctx, onSelect }) {
  const count = item.count?.(ctx);
  const dot = item.dot?.(ctx);
  return (
    <button
      type="button"
      className={`nav-item ${active ? "on" : ""}`.trim()}
      aria-current={active ? "page" : undefined}
      data-view={item.id}
      onClick={() => onSelect(item.id)}
    >
      <Icon name={item.icon} size={16} className="nav-ic" />
      <span className="nav-label">{item.label}</span>
      {count ? (
        <em className="nav-count" title={item.countTitle}>
          {count}
        </em>
      ) : dot ? (
        <i className={`nav-dot nav-dot-${dot.tone}`} title={dot.title} />
      ) : null}
    </button>
  );
}

/** Where the API listens — or which server this page talks to — at the sidebar's foot. */
function StatusLine({ ctx }) {
  let tone = "green";
  let label = "";
  let title = "";
  if (ctx.host.desktop && ctx.settings?.api) {
    const api = ctx.settings.api;
    const address = (api.url || `http://${api.host}:${api.port}`).replace(/^https?:\/\//, "").replace(/\/$/, "");
    if (!api.enabled) {
      tone = "grey";
      label = "API 已关闭";
      title = "本地 API 未启用";
    } else if (api.running === false) {
      tone = "amber";
      label = `API ${address}`;
      title = "本地 API 未在运行";
    } else {
      label = `API ${address}`;
      title = `本地 API 运行中：${api.url || address}`;
    }
  } else if (ctx.host.desktop) {
    tone = "grey";
    label = "API";
  } else {
    label = `服务 ${location.host}`;
    title = `Marginalia ${ctx.host.version || ""} 服务`;
  }
  return (
    <button type="button" className={`side-status ${tone === "green" ? "" : "off"}`.trim()} title={title} onClick={() => ctx.go("api")}>
      <StatusDot tone={tone} ring={tone !== "grey"} />
      <span>{label}</span>
    </button>
  );
}

/**
 * The app's left column: the brand (beside the traffic lights in the desktop
 * app), the pages in groups with a highlight that glides to the open one,
 * then Settings and the status line at the foot.
 */
export default function Sidebar({ onNavigate }) {
  const ctx = useApp();
  const { route } = ctx;
  const list = useRef(null);
  const scroller = useRef(null);
  const [hl, setHl] = useState(null);
  const [glide, setGlide] = useState(false);

  const select = (view) => {
    onNavigate?.();
    if (view === "notes") {
      if (route.view !== "notes") ctx.go("notes");
      return;
    }
    if (view === "home") ctx.goHome();
    else ctx.go(view);
  };

  useLayoutEffect(() => {
    const box = list.current;
    if (!box) return;
    // Measure against the list itself (not offsetTop, whose offsetParent may differ) and again
    // whenever the list reflows: group labels, badges and late font loads all shift the items.
    const measure = () => {
      const el = box.querySelector(`.nav-item[data-view="${route.view}"]`);
      if (!el) return setHl(null);
      const top = Math.round(el.getBoundingClientRect().top - box.getBoundingClientRect().top);
      const height = el.offsetHeight;
      setHl((prev) => (prev && prev.top === top && prev.height === height ? prev : { top, height }));
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(box);
    document.fonts?.ready.then(measure).catch(() => {});
    return () => observer.disconnect();
  }, [route.view]);

  useLayoutEffect(() => {
    const frame = requestAnimationFrame(() => setGlide(true));
    return () => cancelAnimationFrame(frame);
  }, []);

  // The page list hides its scrollbar; when pages lie below the fold (a short
  // window, Settings open with its sections) it fades out at the bottom instead.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return undefined;
    const mark = () => {
      const more = el.scrollTop + el.clientHeight < el.scrollHeight - 1 ? "end" : "";
      if (el.dataset.more !== more) el.dataset.more = more;
    };
    mark();
    el.addEventListener("scroll", mark, { passive: true });
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(mark);
    observer?.observe(el);
    if (list.current) observer?.observe(list.current);
    return () => {
      el.removeEventListener("scroll", mark);
      observer?.disconnect();
    };
  }, [route.view]);

  const subs = subsFor(route.view, ctx);
  const currentSub = route.sub || subs[0]?.id;

  return (
    <aside className="side" aria-label="导航">
      <div className="side-top">
        {ctx.host.native ? (
          // Beside the traffic lights the row is the window's drag handle; 首页 is the first page below.
          <div className="brand">
            <Logo size={16} />
            <span>Marginalia</span>
          </div>
        ) : (
          <button type="button" className="brand" onClick={() => select("home")} title="首页">
            <Logo size={16} />
            <span>Marginalia</span>
          </button>
        )}
      </div>
      <nav className="side-scroll" ref={scroller}>
        <div ref={list} className={`side-nav ${glide ? "glide" : ""}`.trim()}>
          {hl && <span className="nav-hl" style={{ transform: `translateY(${hl.top}px)`, height: hl.height }} aria-hidden="true" />}
          {NAV_GROUPS.map((group) => {
            const items = group.items.filter((item) => !item.when || item.when(ctx));
            if (!items.length) return null;
            return (
              <div key={group.id} className="nav-group" role="group" aria-label={group.label}>
                <div className="nav-gl">{group.label}</div>
                {items.map((item) => (
                  <NavItem key={item.id} item={item} ctx={ctx} active={route.view === item.id} onSelect={select} />
                ))}
              </div>
            );
          })}
        </div>
      </nav>
      <div className="side-foot">
        <NavItem item={SETTINGS_NAV} ctx={ctx} active={route.view === "settings"} onSelect={select} />
        {route.view === "settings" && subs.length > 0 && (
          <div className="nav-sub" role="group" aria-label="设置分区">
            {subs.map((sub) => (
              <button
                key={sub.id}
                type="button"
                className={`nav-s ${currentSub === sub.id ? "cur" : ""}`.trim()}
                aria-current={currentSub === sub.id ? "page" : undefined}
                onClick={() => {
                  onNavigate?.();
                  ctx.go("settings", sub.id === subs[0].id ? null : sub.id);
                }}
              >
                <span>{sub.label}</span>
              </button>
            ))}
          </div>
        )}
        <StatusLine ctx={ctx} />
      </div>
    </aside>
  );
}
