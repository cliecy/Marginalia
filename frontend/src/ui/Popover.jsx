import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import Icon from "./Icon.jsx";
import { IconButton } from "./Button.jsx";
import { formatKeys } from "./Kbd.jsx";

const MARGIN = 8;
const EXIT_MS = 150;

/** Resolves an anchor (Element, rect-like, or {x, y} point) to a DOMRect-like box. */
function anchorRect(anchor) {
  if (!anchor) return null;
  if (typeof anchor.getBoundingClientRect === "function") return anchor.getBoundingClientRect();
  if (typeof anchor.x === "number" && typeof anchor.y === "number" && anchor.width === undefined) {
    return { left: anchor.x, right: anchor.x, top: anchor.y, bottom: anchor.y, width: 0, height: 0 };
  }
  return anchor;
}

/**
 * Keeps rendering for a moment after `open` turns false, so the leaving
 * animation can play. Returns [mounted, leaving].
 */
function usePresence(open, ms = EXIT_MS) {
  const [mounted, setMounted] = useState(open);
  useEffect(() => {
    if (open) {
      setMounted(true);
      return undefined;
    }
    const timer = setTimeout(() => setMounted(false), ms);
    return () => clearTimeout(timer);
  }, [open, ms]);
  return [open || mounted, !open && mounted];
}

/**
 * A floating panel anchored to an element or a point: frosted `--pop-bg`,
 * radius 11, `--shadow`, growing out of its anchor. It flips above when
 * there is no room below, stays inside the window, and closes on Escape or a
 * click outside.
 *
 * @param {object} props
 * @param {boolean} props.open
 * @param {Element|DOMRect|{x:number,y:number}} props.anchor
 * @param {() => void} props.onClose
 * @param {"bottom-start"|"bottom-end"|"bottom"|"top-start"|"top-end"|"top"} [props.placement]
 * @param {number} [props.offset] gap to the anchor (default 6)
 * @param {number|string} [props.width] fixed width (default: content width, min 180)
 * @param {boolean} [props.matchWidth] at least as wide as the anchor
 */
export function Popover({
  open,
  anchor,
  onClose,
  placement = "bottom-start",
  offset = 6,
  width,
  matchWidth = false,
  className = "",
  role = "dialog",
  ariaLabel,
  children,
}) {
  const [mounted, leaving] = usePresence(open);
  const panel = useRef(null);
  const last = useRef({ anchor, children });
  if (open) last.current = { anchor, children };
  const shownAnchor = open ? anchor : last.current.anchor;
  const shownChildren = open ? children : last.current.children;
  const [pos, setPos] = useState(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  // Focus goes back where it was when the popover closes, unless something
  // else took it meanwhile (the editor, a field the chosen item focused…).
  // Both happen in layout effects: noted before the menu moves focus into the
  // panel (it does in an effect), given back after React's own focus
  // bookkeeping for the commit — and before a dialog the chosen item opened
  // notes where its focus should return to (DialogHost comes after the menus).
  const returnTo = useRef(null);
  useLayoutEffect(() => {
    if (open) {
      const active = document.activeElement;
      returnTo.current =
        active && active !== document.body && !panel.current?.contains(active)
          ? active
          : anchor && typeof anchor.focus === "function"
            ? anchor
            : null;
      return;
    }
    const back = returnTo.current;
    returnTo.current = null;
    const now = document.activeElement;
    const lost = !now || now === document.body || !!panel.current?.contains(now);
    if (lost && back?.isConnected && typeof back.focus === "function") back.focus({ preventScroll: true });
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const place = useCallback(() => {
    const node = panel.current;
    const rect = anchorRect(shownAnchor);
    if (!node || !rect) return;
    const vw = window.innerWidth;
    const vh = window.innerHeight;
    const w = node.offsetWidth;
    const natural = node.scrollHeight;
    const [side, align = "center"] = placement.split("-");
    const below = vh - rect.bottom - offset - MARGIN;
    const above = rect.top - offset - MARGIN;
    const up = side === "top" ? !(above < Math.min(natural, 240) && below > above) : below < Math.min(natural, 240) && above > below;
    const room = Math.max(120, up ? above : below);
    const h = Math.min(natural, room);
    let left = align === "start" ? rect.left : align === "end" ? rect.right - w : rect.left + rect.width / 2 - w / 2;
    left = Math.min(Math.max(MARGIN, left), Math.max(MARGIN, vw - w - MARGIN));
    const top = up ? rect.top - offset - h : rect.bottom + offset;
    const ox = Math.min(Math.max(rect.left + rect.width / 2 - left, 12), w - 12);
    const next = { left, top, maxHeight: room, up, ox, minWidth: matchWidth ? rect.width : undefined };
    setPos((prev) =>
      prev && Object.keys(next).every((key) => (typeof next[key] === "number" ? Math.abs(next[key] - prev[key]) < 0.5 : next[key] === prev[key])) ? prev : next,
    );
  }, [shownAnchor, placement, offset, matchWidth]);

  useLayoutEffect(() => {
    if (!mounted) {
      setPos(null);
      return;
    }
    place();
  }, [mounted, place, shownChildren]);

  useEffect(() => {
    if (!open) return undefined;
    const anchorEl = anchor && typeof anchor.contains === "function" ? anchor : null;
    const onPointer = (event) => {
      const target = event.target;
      if (panel.current?.contains(target)) return;
      if (anchorEl?.contains(target)) return;
      closeRef.current?.();
    };
    const onKey = (event) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        closeRef.current?.();
      }
    };
    const onMove = () => place();
    // Defer so the click that opened the popover doesn't close it.
    const timer = setTimeout(() => document.addEventListener("pointerdown", onPointer, true), 0);
    document.addEventListener("keydown", onKey, true);
    window.addEventListener("resize", onMove);
    window.addEventListener("scroll", onMove, true);
    window.addEventListener("blur", onPointer);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("pointerdown", onPointer, true);
      document.removeEventListener("keydown", onKey, true);
      window.removeEventListener("resize", onMove);
      window.removeEventListener("scroll", onMove, true);
      window.removeEventListener("blur", onPointer);
    };
  }, [open, anchor, place]);

  if (!mounted || typeof document === "undefined") return null;
  const style = {
    left: pos?.left ?? -9999,
    top: pos?.top ?? -9999,
    maxHeight: pos?.maxHeight,
    width,
    minWidth: pos?.minWidth,
    // unplaced for one frame: invisible but focusable, so a menu or search field can take focus at once
    opacity: pos ? undefined : 0,
    pointerEvents: pos ? undefined : "none",
    "--ox": pos ? `${pos.ox}px` : undefined,
  };
  return createPortal(
    <div
      ref={panel}
      role={role}
      aria-label={ariaLabel}
      className={`pop ${pos?.up ? "up" : ""} ${leaving ? "leaving" : ""} ${className}`.replace(/\s+/g, " ").trim()}
      style={style}
    >
      {shownChildren}
    </div>,
    document.body,
  );
}

/**
 * A menu of actions inside a Popover. Up/Down move, Enter picks, Escape closes.
 *
 * Item shape:
 *   { label, icon?, hint?, kbd?, danger?, disabled?, checked?, onSelect }
 *   { divider: true }
 *   { heading: "分组名" }
 */
export function Menu({ items, onClose, className = "" }) {
  const list = useRef(null);
  const visible = items.filter(Boolean);

  useEffect(() => {
    const first = list.current?.querySelector("button:not(:disabled)");
    first?.focus({ preventScroll: true });
  }, []);

  const onKeyDown = (event) => {
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const buttons = [...list.current.querySelectorAll("button:not(:disabled)")];
    const index = buttons.indexOf(document.activeElement);
    let next = index;
    if (event.key === "ArrowDown") next = (index + 1) % buttons.length;
    if (event.key === "ArrowUp") next = (index - 1 + buttons.length) % buttons.length;
    if (event.key === "Home") next = 0;
    if (event.key === "End") next = buttons.length - 1;
    buttons[next]?.focus();
  };

  return (
    <div ref={list} role="menu" className={`menu ${className}`.trim()} onKeyDown={onKeyDown}>
      {visible.map((item, index) => {
        if (item.divider) return <div key={`d${index}`} className="menu-sep" role="separator" />;
        if (item.heading) return <div key={`h${index}`} className="menu-head">{item.heading}</div>;
        return (
          <button
            key={item.id ?? `${index}-${typeof item.label === "string" ? item.label : ""}`}
            type="button"
            role={item.checked !== undefined ? "menuitemcheckbox" : "menuitem"}
            aria-checked={item.checked !== undefined ? !!item.checked : undefined}
            className={`menu-item ${item.danger ? "danger" : ""} ${item.hint ? "has-hint" : ""}`.replace(/\s+/g, " ").trim()}
            disabled={item.disabled}
            onClick={() => {
              onClose?.();
              item.onSelect?.();
            }}
          >
            {item.icon ? <Icon name={item.icon} size={15} /> : item.checked !== undefined ? <span className="menu-ic-pad" /> : null}
            <span className="menu-words">
              <span className="menu-label">{item.label}</span>
              {item.hint && <span className="menu-hint">{item.hint}</span>}
            </span>
            {item.kbd && <span className="menu-kbd">{formatKeys(item.kbd)}</span>}
            {item.checked && <Icon name="check" size={14} className="menu-check" />}
          </button>
        );
      })}
    </div>
  );
}

/**
 * A button that opens a Menu. Without children it is an IconButton (default
 * "more"); with children, a ghost text button showing them.
 *
 * @param {object} props
 * @param {Array|(() => Array)} props.items menu items (a function is called on open)
 * @param {string} [props.icon]
 * @param {string} [props.label] tooltip and accessible name
 * @param {string} [props.placement]
 * @param {string} [props.buttonClassName]
 */
export function MenuButton({ items, icon = "more", label = "更多", placement = "bottom-end", children, buttonClassName = "", disabled, size = "md", width }) {
  const button = useRef(null);
  const [open, setOpen] = useState(false);
  const [resolved, setResolved] = useState([]);
  const toggle = () => {
    if (!open) setResolved(typeof items === "function" ? items() : items);
    setOpen(!open);
  };
  return (
    <>
      {children ? (
        <button ref={button} type="button" className={`menu-trigger ${open ? "open" : ""} ${buttonClassName}`.trim()} aria-haspopup="menu" aria-expanded={open} title={label} disabled={disabled} onClick={toggle}>
          {children}
        </button>
      ) : (
        <IconButton ref={button} icon={icon} label={label} size={size} active={open} aria-haspopup="menu" aria-expanded={open} disabled={disabled} onClick={toggle} className={buttonClassName} />
      )}
      <Popover open={open} anchor={button.current} onClose={() => setOpen(false)} placement={placement} role="presentation" width={width}>
        <Menu items={resolved} onClose={() => setOpen(false)} />
      </Popover>
    </>
  );
}

// ------------------------------------------------------------ imperative menus
let menuState = null;
let menuSeq = 0;
const menuListeners = new Set();
const emitMenu = () => menuListeners.forEach((fn) => fn());

function release(state) {
  state?.options.mark?.removeAttribute("data-menu-open");
  state?.options.onClose?.();
}

/**
 * Opens a menu at an element or a point, from anywhere — the context menus of
 * the file tree use it. Needs <MenuHost/> mounted once (App does).
 *
 * @param {Element|{x:number,y:number}} anchor
 * @param {Array} items Menu items
 * @param {{placement?: string, mark?: Element, onClose?: () => void}} [options]
 *        mark: an element that carries data-menu-open while the menu shows (the row it is for)
 */
export function openMenu(anchor, items, options = {}) {
  release(menuState);
  options.mark?.setAttribute("data-menu-open", "");
  menuState = { anchor, items, options, key: ++menuSeq };
  emitMenu();
}

/** Closes the menu openMenu showed. */
export function closeMenu() {
  if (!menuState) return;
  release(menuState);
  menuState = null;
  emitMenu();
}

/** Shows `items` as a context menu at the pointer of a contextmenu event, marking the element it was opened on. */
export function contextMenu(event, items) {
  event.preventDefault();
  event.stopPropagation();
  openMenu({ x: event.clientX, y: event.clientY }, items, { placement: "bottom-start", point: true, mark: event.currentTarget });
}

/** Renders the menu of openMenu/contextMenu. Mount once near the root. */
export function MenuHost() {
  const state = useSyncExternalStore(
    (fn) => {
      menuListeners.add(fn);
      return () => menuListeners.delete(fn);
    },
    () => menuState,
  );
  return (
    <Popover open={!!state} anchor={state?.anchor} onClose={closeMenu} placement={state?.options.placement ?? "bottom-start"} offset={state?.options.point ? 2 : 6} role="presentation">
      {state && <Menu key={state.key} items={state.items} onClose={closeMenu} />}
    </Popover>
  );
}

export default Popover;
