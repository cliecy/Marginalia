import { forwardRef, useEffect, useImperativeHandle, useLayoutEffect, useRef } from "react";
import { enhance, theme } from "../lib/rich.js";
import { copyText } from "../ui/CopyField.jsx";

// ------------------------------------------------------------------ scheme
// Mermaid diagrams and ```visual frames are drawn for one colour scheme. One
// shared watcher tells every live Rich block when the scheme flips (the app's
// own switch dispatches "mn-theme"; the system one changes the media query).
const schemeListeners = new Set();
let watching = false;
let lastScheme = null;

function watchScheme() {
  if (watching || typeof window === "undefined") return;
  watching = true;
  lastScheme = theme();
  const check = () => {
    // data-theme is set right before "mn-theme" fires; read it on the next frame to be safe.
    requestAnimationFrame(() => {
      const next = theme();
      if (next === lastScheme) return;
      lastScheme = next;
      schemeListeners.forEach((fn) => fn(next));
    });
  };
  window.addEventListener("mn-theme", check);
  window.matchMedia?.("(prefers-color-scheme: dark)").addEventListener?.("change", check);
}

/** Re-renders the diagrams and visuals inside `root` (after a scheme change). */
function redraw(root) {
  if (!root) return;
  for (const el of root.querySelectorAll(".rich-block[data-done]")) delete el.dataset.done;
  enhance(root);
}

// ------------------------------------------------------------- code tools
const COPY_ICON =
  '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="8.5" y="8.5" width="12" height="12" rx="2.5"/><path d="M15.5 8.5V5.5a2 2 0 0 0-2-2h-8a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h3"/></svg>';
const CHECK_ICON =
  '<svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m5 12.5 4.5 4.5L19 7.5"/></svg>';

/** Gives every highlighted code block a copy button (top right, shown on hover). */
function addCodeTools(root) {
  if (!root) return;
  for (const pre of root.querySelectorAll("pre.hljs:not([data-tools])")) {
    pre.dataset.tools = "1";
    const button = document.createElement("button");
    button.type = "button";
    button.className = "code-copy";
    button.title = "复制代码";
    button.setAttribute("aria-label", "复制代码");
    button.innerHTML = COPY_ICON;
    let timer = 0;
    button.addEventListener("click", async (event) => {
      event.preventDefault();
      event.stopPropagation();
      const code = pre.querySelector("code")?.innerText ?? pre.innerText;
      if (!(await copyText(code.replace(/\n$/, "")))) return;
      button.classList.add("done");
      button.innerHTML = CHECK_ICON;
      clearTimeout(timer);
      timer = setTimeout(() => {
        button.classList.remove("done");
        button.innerHTML = COPY_ICON;
      }, 1400);
    });
    pre.appendChild(button);
  }
}

// ------------------------------------------------------- Obsidian extras
const TASK = /^\[([ xX])\]\s+/;

/**
 * Turns "- [ ] item" / "- [x] item" list items (rendered as plain text) into
 * task rows with a box in place of the bullet, as Obsidian shows them.
 */
function addTaskBoxes(root) {
  for (const li of root.querySelectorAll("li:not(.task-item)")) {
    // Tight lists hold the text directly, loose ones in a <p> (after a line break).
    let holder = li;
    let first = li.firstChild;
    while (first?.nodeType === Node.TEXT_NODE && !first.data.trim()) first = first.nextSibling;
    if (first?.nodeName === "P") {
      holder = first;
      first = first.firstChild;
    }
    if (first?.nodeType !== Node.TEXT_NODE) continue;
    const match = TASK.exec(first.data);
    if (!match) continue;
    const done = match[1] !== " ";
    first.data = first.data.slice(match[0].length);
    const box = document.createElement("span");
    box.className = `task-box${done ? " done" : ""}`;
    box.setAttribute("role", "img");
    box.setAttribute("aria-label", done ? "已完成" : "待办");
    holder.insertBefore(box, first);
    li.classList.add("task-item");
    li.classList.toggle("done", done);
    li.parentElement?.classList.add("task-list");
  }
}

const MARK = /==([^=\s](?:[^\n]*?[^=\s])?)==/g;
const NO_MARKS = "code, pre, mark, .katex, .rich-block, .callout-title, textarea, script, style";

/** Wraps Obsidian's ==highlights== (left as text by the renderer) in <mark>. */
function addMarks(root) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (node) => (node.data.includes("==") && !node.parentElement?.closest(NO_MARKS) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT),
  });
  const found = [];
  while (walker.nextNode()) found.push(walker.currentNode);
  for (const node of found) {
    const parts = node.data.split(MARK);
    if (parts.length < 3) continue;
    const fragment = document.createDocumentFragment();
    parts.forEach((part, i) => {
      if (i % 2) {
        const mark = document.createElement("mark");
        mark.textContent = part;
        fragment.append(mark);
      } else if (part) fragment.append(part);
    });
    node.replaceWith(fragment);
  }
}

/**
 * Sanitised Markdown HTML whose rich parts come alive after mount: Mermaid
 * diagrams, sandboxed ```visual / ```chart frames (lib/rich.js), copy buttons
 * on code blocks, Obsidian task boxes and ==highlights==. Diagrams redraw when
 * the colour scheme changes.
 *
 * @param {object} props
 * @param {string} props.html   sanitised HTML (lib/markdown.js renderMarkdown)
 * @param {boolean} [props.live] render diagrams and tools (false while an answer is still streaming)
 * @param {string} [props.className]
 * @param {Function} [props.onClick]
 */
const Rich = forwardRef(function Rich({ html, live = true, className = "", onClick }, ref) {
  const host = useRef(null);
  useImperativeHandle(ref, () => host.current);

  // The HTML is written here rather than through dangerouslySetInnerHTML: React
  // re-applies that prop on every parent render (a new {__html} object), which
  // would wipe the rendered diagrams and tools without re-running this effect.
  useLayoutEffect(() => {
    const root = host.current;
    if (!root) return;
    root.innerHTML = html;
    if (!live) return;
    enhance(root);
    addCodeTools(root);
    addTaskBoxes(root);
    addMarks(root);
  }, [html, live]);

  useEffect(() => {
    if (!live) return undefined;
    watchScheme();
    const onScheme = () => redraw(host.current);
    schemeListeners.add(onScheme);
    return () => {
      schemeListeners.delete(onScheme);
    };
  }, [live]);

  return <div ref={host} className={className} onClick={onClick} />;
});

export default Rich;
