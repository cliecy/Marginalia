import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { renderMarkdown, splitFrontmatter } from "../lib/markdown.js";
import Icon from "../ui/Icon.jsx";
import Rich from "./Rich.jsx";

/** Split on `---` lines outside code fences; fall back to H1/H2 boundaries. */
export function splitSlides(text) {
  const { body } = splitFrontmatter(text || "");
  const lines = body.split("\n");
  const cut = (isBreak, keepLine) => {
    const slides = [];
    let current = [];
    let fence = null;
    for (const line of lines) {
      const marker = line.match(/^\s*(`{3,}|~{3,})/)?.[1];
      if (marker) fence = fence ? (marker[0] === fence ? null : fence) : marker[0];
      if (!fence && isBreak(line) && current.join("").trim()) {
        slides.push(current.join("\n"));
        current = keepLine ? [line] : [];
        continue;
      }
      if (!fence && isBreak(line) && !keepLine) continue;
      current.push(line);
    }
    if (current.join("").trim()) slides.push(current.join("\n"));
    return slides;
  };
  const byRule = cut((l) => /^\s*---\s*$/.test(l), false);
  if (byRule.length > 1) return byRule;
  return cut((l) => /^#{1,2}\s/.test(l), true);
}

const isTitleSlide = (md) => {
  const text = md.trim();
  return /^#{1,3}\s/.test(text) && text.length < 220 && !/^\s*([-*+]|\d+\.)\s|```|\|/m.test(text);
};

/** Expand / collapse glyphs for the full-screen button (not in the app's icon set). */
const FullIcon = ({ exit }) => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {exit ? <path d="M9 4v5H4M15 4v5h5M9 20v-5H4M15 20v-5h5" /> : <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" />}
  </svg>
);

const IDLE_MS = 2400;

/**
 * Presents a note as slides: pages split on `---` (or H1/H2 headings), one
 * 16:9 card on the window's ground, a thin progress line, and a floating
 * control pill that fades away while the pointer rests.
 *
 * Keys: → ↓ Space PageDown Enter next · ← ↑ PageUp Backspace back · Home/End ·
 * F full screen · Esc leave. A click on the left third goes back, elsewhere forward.
 *
 * @param {object} props
 * @param {string} props.content
 * @param {string} [props.path]
 * @param {import("../lib/markdown.js").LinkResolver} [props.resolver]
 * @param {() => void} props.onClose
 */
export default function Slides({ content, path, resolver, onClose }) {
  const slides = useMemo(() => splitSlides(content), [content]);
  const count = Math.max(slides.length, 1);
  const [index, setIndex] = useState(0);
  const [direction, setDirection] = useState(1);
  const [fullscreen, setFullscreen] = useState(false);
  const [idle, setIdle] = useState(false);
  const root = useRef(null);
  const idleTimer = useRef(0);
  const current = slides[Math.min(index, slides.length - 1)] || "";
  const html = useMemo(() => renderMarkdown(current, { path, resolver, frontmatter: false }), [current, path, resolver]);

  // The deck can shrink while presenting (the note was edited): stay on a real slide.
  useEffect(() => {
    if (index > slides.length - 1) setIndex(Math.max(slides.length - 1, 0));
  }, [index, slides.length]);

  const go = useCallback(
    (delta) => {
      setDirection(delta < 0 ? -1 : 1);
      setIndex((i) => Math.min(Math.max(i + delta, 0), count - 1));
    },
    [count],
  );
  const jump = useCallback(
    (target) => {
      const next = Math.min(Math.max(target, 0), count - 1);
      setDirection(next < index ? -1 : 1);
      setIndex(next);
    },
    [count, index],
  );

  const toggleFullscreen = useCallback(() => {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else root.current?.requestFullscreen?.().catch(() => {});
  }, []);

  const wake = useCallback(() => {
    setIdle(false);
    clearTimeout(idleTimer.current);
    idleTimer.current = setTimeout(() => setIdle(true), IDLE_MS);
  }, []);

  useEffect(() => {
    root.current?.focus();
    wake();
    return () => clearTimeout(idleTimer.current);
  }, [wake]);

  useEffect(() => {
    const onKey = (event) => {
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (["ArrowRight", "ArrowDown", "PageDown", " ", "Enter"].includes(event.key)) (event.preventDefault(), go(1));
      else if (["ArrowLeft", "ArrowUp", "PageUp", "Backspace"].includes(event.key)) (event.preventDefault(), go(-1));
      else if (event.key === "Home") (event.preventDefault(), jump(0));
      else if (event.key === "End") (event.preventDefault(), jump(count - 1));
      else if (event.key === "Escape" && !document.fullscreenElement) onClose();
      else if (event.key === "f" || event.key === "F") toggleFullscreen();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
    };
  }, [go, jump, onClose, count, toggleFullscreen]);

  useEffect(() => {
    const onChange = () => {
      setFullscreen(!!document.fullscreenElement);
      if (!document.fullscreenElement) root.current?.focus();
    };
    document.addEventListener("fullscreenchange", onChange);
    return () => {
      document.removeEventListener("fullscreenchange", onChange);
    };
  }, []);

  const onStageClick = (event) => {
    if (event.target.closest("a, button, iframe, .rich-block, input, summary, pre")) return;
    const { left, width } = event.currentTarget.getBoundingClientRect();
    go(event.clientX - left < width * 0.3 ? -1 : 1);
  };

  return (
    <div
      className={`slides ${fullscreen ? "is-full" : ""} ${idle ? "is-idle" : ""}`.replace(/\s+/g, " ").trim()}
      ref={root}
      tabIndex={-1}
      role="dialog"
      aria-modal="true"
      aria-label="演示"
      onMouseMove={wake}
    >
      <div className="slide-stage" onClick={onStageClick}>
        <Rich
          key={index}
          html={html}
          className={`markdown slide ${direction < 0 ? "from-left" : "from-right"} ${isTitleSlide(current) ? "title-slide" : ""}`.replace(/\s+/g, " ").trim()}
        />
        <div className="slide-progress" style={{ transform: `scaleX(${(index + 1) / count})` }} aria-hidden="true" />
      </div>
      <div className="slide-controls" role="toolbar" aria-label="演示控制">
        <button type="button" onClick={() => go(-1)} disabled={index === 0} title="上一页（←）" aria-label="上一页">
          <Icon name="chevron-left" size={16} />
        </button>
        <span className="slide-count" aria-live="polite">
          <b>{index + 1}</b> / {count}
        </span>
        <button type="button" onClick={() => go(1)} disabled={index >= count - 1} title="下一页（→）" aria-label="下一页">
          <Icon name="chevron-right" size={16} />
        </button>
        <i className="slide-sep" aria-hidden="true" />
        <button type="button" onClick={toggleFullscreen} title={fullscreen ? "退出全屏（F）" : "全屏（F）"} aria-label={fullscreen ? "退出全屏" : "全屏"}>
          <FullIcon exit={fullscreen} />
        </button>
        <button type="button" onClick={onClose} title="结束演示（Esc）" aria-label="结束演示">
          <Icon name="x" size={15} />
        </button>
      </div>
    </div>
  );
}
