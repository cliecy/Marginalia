import { forwardRef, useDeferredValue, useImperativeHandle, useMemo, useRef } from "react";
import { isLocalHref, renderMarkdown } from "../lib/markdown.js";
import { isDesktop, openExternal } from "../lib/native.js";
import { toast } from "../ui/Toast.jsx";
import Rich from "./Rich.jsx";

const decoded = (text) => {
  try {
    return decodeURIComponent(text);
  } catch {
    return text;
  }
};

/**
 * Routes a click on a link in rendered Markdown (a note or an AI answer):
 * notes open in the workspace, missing notes can be created, web links open in
 * the browser (in the desktop app), and a link into the app's own URL space is
 * never followed — it would reload the app and lose the editor and the AI
 * conversation.
 *
 * @param {MouseEvent} event
 * @param {{onNavigate?: (path: string, anchor?: string) => void, onCreate?: (name: string) => void}} handlers
 * @returns {boolean} whether a link was handled
 */
export function routeLinkClick(event, { onNavigate, onCreate } = {}) {
  const link = event.target.closest("a");
  if (!link) return false;
  const href = link.getAttribute("href") || "";
  if (link.dataset.note) {
    event.preventDefault();
    onNavigate?.(link.dataset.note, link.dataset.anchor);
  } else if (link.dataset.create !== undefined) {
    event.preventDefault();
    onCreate?.(link.dataset.create);
  } else if (link.dataset.missing !== undefined) {
    event.preventDefault();
    toast.warn(`找不到「${link.dataset.missing}」`);
  } else if (isDesktop && /^(https?:|mailto:)/i.test(href)) {
    // The webview would open a second app window; the default browser is where links belong.
    event.preventDefault();
    openExternal(link.href).catch(() => {});
  } else if (isLocalHref(href)) {
    event.preventDefault();
    toast.warn(`找不到「${decoded(href)}」`);
  } else {
    return false;
  }
  return true;
}

/**
 * A note rendered for reading: Obsidian-flavoured Markdown (wikilinks, embeds,
 * callouts, maths, Mermaid, ```visual blocks) in the app's reading typography.
 *
 * Clicks on links are routed (routeLinkClick): [[wikilinks]] and relative links
 * to notes call `onNavigate(path, anchor)`, links to missing notes
 * `onCreate(name)`, in the desktop app external links open in the default
 * browser, and links into the app's own URL space are never followed.
 *
 * Imperative handle: `scrollToHeading(id)` (ids are "h-0", "h-1"… as in outline()).
 *
 * @param {object} props
 * @param {string} props.text
 * @param {string} [props.path]          the note's vault path (resolves relative links)
 * @param {import("../lib/markdown.js").LinkResolver} [props.resolver]
 * @param {(path: string, anchor?: string) => void} [props.onNavigate]
 * @param {(name: string) => void} [props.onCreate]
 * @param {string} [props.className]
 */
const Preview = forwardRef(function Preview({ text, path, resolver, onNavigate, onCreate, className = "" }, ref) {
  const host = useRef(null);
  // Typing in split view shouldn't re-render diagrams on every keystroke.
  const deferred = useDeferredValue(text);
  const html = useMemo(() => renderMarkdown(deferred, { path, resolver }), [deferred, path, resolver]);

  useImperativeHandle(ref, () => ({
    scrollToHeading(id) {
      host.current?.querySelector(`#${CSS.escape(id)}`)?.scrollIntoView({ behavior: "smooth", block: "start" });
    },
  }));

  const onClick = (event) => routeLinkClick(event, { onNavigate, onCreate });

  return <Rich ref={host} html={html} className={`markdown ${className}`.trim()} onClick={onClick} />;
});

export default Preview;
