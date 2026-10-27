/**
 * Turns placeholder blocks produced by the Markdown renderer into live visuals:
 *   ```mermaid           → diagram (rendered here, Mermaid's strict security level)
 *   ```visual/svg/chart  → sandboxed <iframe> (see public/sandbox.html)
 */

import { apiUrl } from "./api.js";

/** The theme in effect: an explicit data-theme on <html> wins over the system preference. */
export const theme = () => {
  const forced = typeof document !== "undefined" ? document.documentElement.dataset.theme : "";
  if (forced === "dark" || forced === "light") return forced;
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
};

const decode = (el) => {
  try {
    return decodeURIComponent(el.dataset.code || "");
  } catch {
    return "";
  }
};

// ---------------------------------------------------------------- mermaid
let mermaidPromise = null;
let mermaidTheme = null;
let mermaidCounter = 0;
const svgCache = new Map();

async function loadMermaid() {
  mermaidPromise ??= import("mermaid").then((m) => m.default);
  const mermaid = await mermaidPromise;
  const current = theme();
  if (mermaidTheme !== current) {
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: "strict",
      theme: current === "dark" ? "dark" : "default",
      fontFamily: getComputedStyle(document.body).fontFamily,
    });
    mermaidTheme = current;
  }
  return mermaid;
}

// Mermaid keeps global state while it draws: overlapping render() calls (the
// live preview re-renders on every keystroke) can come back empty. Run them one
// at a time.
let mermaidQueue = Promise.resolve();
function renderSerially(code) {
  const run = mermaidQueue.then(async () => {
    const mermaid = await loadMermaid();
    return mermaid.render(`mn-mermaid-${++mermaidCounter}`, code);
  });
  mermaidQueue = run.catch(() => {});
  return run;
}

async function renderMermaid(el) {
  const code = decode(el);
  const key = `${theme()}\n${code}`;
  if (svgCache.has(key)) {
    el.innerHTML = svgCache.get(key);
    return;
  }
  el.classList.add("loading");
  try {
    const { svg } = await renderSerially(code);
    if (!svg?.includes("<svg")) throw new Error("渲染结果为空");
    svgCache.set(key, svg);
    if (svgCache.size > 200) svgCache.delete(svgCache.keys().next().value);
    el.innerHTML = svg;
  } catch (err) {
    el.innerHTML = "";
    const box = document.createElement("div");
    box.className = "rich-error";
    box.textContent = `Mermaid 图表有语法错误：${String(err?.message || err).split("\n")[0]}`;
    const pre = document.createElement("pre");
    pre.textContent = code;
    el.append(box, pre);
    document.getElementById(`dmn-mermaid-${mermaidCounter}`)?.remove(); // mermaid leaves an error svg behind
  } finally {
    el.classList.remove("loading");
  }
}

// ----------------------------------------------------------------- visual
const frames = new Map(); // contentWindow → { iframe, kind, code, key }
const heights = new Map(); // remembered heights so re-renders don't jump

if (typeof window !== "undefined") {
  window.addEventListener("message", (event) => {
    const entry = frames.get(event.source);
    if (!entry || !event.data) return;
    if (event.data.type === "mn-ready") {
      event.source.postMessage({ type: "mn-render", kind: entry.kind, code: entry.code, theme: theme() }, "*");
    } else if (event.data.type === "mn-height") {
      const h = Math.min(Math.max(Number(event.data.h) || 0, 40), 6000);
      entry.iframe.style.height = `${h}px`;
      heights.set(entry.key, h);
    }
  });
}

function renderVisual(el) {
  const code = decode(el);
  const kind = el.dataset.kind || "visual";
  const key = `${kind}\n${code}`;

  const iframe = document.createElement("iframe");
  iframe.setAttribute("sandbox", "allow-scripts");
  iframe.setAttribute("loading", "lazy");
  iframe.setAttribute("title", "可视化");
  iframe.className = "visual-frame";
  iframe.style.height = `${heights.get(key) || 160}px`;

  const bar = document.createElement("div");
  bar.className = "rich-bar";
  const source = document.createElement("button");
  source.textContent = "源码";
  const full = document.createElement("button");
  full.textContent = "全屏";
  bar.append(source, full);

  const pre = document.createElement("pre");
  pre.className = "rich-source";
  pre.textContent = code;
  pre.hidden = true;

  source.onclick = () => {
    pre.hidden = !pre.hidden;
    source.textContent = pre.hidden ? "源码" : "隐藏源码";
  };
  full.onclick = () => (document.fullscreenElement ? document.exitFullscreen() : el.requestFullscreen?.());

  el.replaceChildren(bar, iframe, pre);
  iframe.src = apiUrl("/sandbox.html");
  // contentWindow exists as soon as the frame is in the document.
  frames.set(iframe.contentWindow, { iframe, kind, code, key });
}

/** Render every not-yet-rendered rich block inside `root`. */
export function enhance(root) {
  if (!root) return;
  for (const [win, entry] of frames) if (!entry.iframe.isConnected) frames.delete(win);
  for (const el of root.querySelectorAll(".mermaid-block:not([data-done])")) {
    el.dataset.done = "1";
    renderMermaid(el);
  }
  for (const el of root.querySelectorAll(".visual-block:not([data-done])")) {
    el.dataset.done = "1";
    renderVisual(el);
  }
}
