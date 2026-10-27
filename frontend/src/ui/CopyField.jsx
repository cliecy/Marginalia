import { useEffect, useRef, useState } from "react";
import Icon from "./Icon.jsx";

/** Copies text to the clipboard (with a fallback for webviews without the async API). */
export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    let ok = false;
    try {
      ok = document.execCommand("copy");
    } catch {}
    area.remove();
    return ok;
  }
}

/**
 * A 22px copy button that turns into a green tick for a moment.
 *
 * @param {object} props
 * @param {string | (() => string)} props.text what to copy
 * @param {string} [props.label] tooltip (default "复制")
 */
export function CopyButton({ text, label = "复制", className = "", size = 13 }) {
  const [done, setDone] = useState(false);
  const timer = useRef(null);
  useEffect(() => () => clearTimeout(timer.current), []);
  const onClick = async (event) => {
    event.stopPropagation();
    const ok = await copyText(typeof text === "function" ? text() : text);
    if (!ok) return;
    setDone(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setDone(false), 1400);
  };
  return (
    <button type="button" className={`copy-btn ${done ? "done" : ""} ${className}`.replace(/\s+/g, " ").trim()} title={done ? "已复制" : label} aria-label={label} onClick={onClick}>
      <Icon name={done ? "check" : "copy"} size={size} stroke={done ? 1.9 : 1.5} />
    </button>
  );
}

/**
 * A value to read and copy: a pill holding monospace text, a copy button, and
 * for secrets an eye that reveals it (Magpie's connect card `.val`).
 *
 * @param {object} props
 * @param {string} props.value
 * @param {boolean} [props.secret] masked until the eye is pressed
 * @param {boolean} [props.mono] monospace (default true)
 * @param {React.ReactNode} [props.actions] extra buttons after copy (e.g. rotate)
 * @param {boolean} [props.block] take the full width
 */
export function CopyField({ value, secret = false, mono = true, actions, block = false, className = "", placeholder = "—" }) {
  const [shown, setShown] = useState(!secret);
  const text = value ?? "";
  const display = !text ? placeholder : shown ? text : "•".repeat(Math.min(Math.max(text.length, 8), 24));
  return (
    <span className={`copy-field ${block ? "block" : ""} ${className}`.replace(/\s+/g, " ").trim()}>
      <code className={`${mono ? "" : "plain"} ${shown ? "" : "masked"}`.trim()} title={shown ? text : undefined}>
        {display}
      </code>
      {secret && text && (
        <button type="button" className="copy-btn" title={shown ? "隐藏" : "显示"} aria-label={shown ? "隐藏" : "显示"} onClick={() => setShown(!shown)}>
          <Icon name={shown ? "eye-off" : "eye"} size={13} />
        </button>
      )}
      {text && <CopyButton text={text} />}
      {actions}
    </span>
  );
}

export default CopyField;
