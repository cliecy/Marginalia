import { useCallback, useLayoutEffect, useRef, useState } from "react";
import Icon from "./Icon.jsx";

/**
 * A segmented control: a pill track with a raised thumb that glides to the
 * picked option (Magpie's `.segs`). Arrow keys move the choice.
 *
 * @param {object} props
 * @param {string} props.value
 * @param {(value: string) => void} props.onChange
 * @param {Array<{value: string, label?: React.ReactNode, icon?: string, count?: number, title?: string, disabled?: boolean}>} props.options
 * @param {"sm"|"md"} [props.size] md = 24px buttons, 12.5px text; sm = 20px, 11.5px
 * @param {boolean} [props.fill] stretch to the container's width, options sharing it equally
 * @param {string} [props.ariaLabel]
 */
export default function Segmented({ value, onChange, options, size = "md", fill = false, ariaLabel, className = "" }) {
  const track = useRef(null);
  const [thumb, setThumb] = useState(null);
  const [ready, setReady] = useState(false);

  const measure = useCallback(() => {
    const el = track.current?.querySelector('[aria-checked="true"]');
    if (!el) return setThumb(null);
    setThumb((prev) => {
      const next = { left: el.offsetLeft, width: el.offsetWidth };
      return prev && prev.left === next.left && prev.width === next.width ? prev : next;
    });
  }, []);

  useLayoutEffect(() => {
    measure();
  }, [measure, value, options]);

  useLayoutEffect(() => {
    // The first placement doesn't glide in from the left edge.
    const frame = requestAnimationFrame(() => setReady(true));
    const node = track.current;
    if (!node || typeof ResizeObserver === "undefined") return () => cancelAnimationFrame(frame);
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
    };
  }, [measure]);

  const onKeyDown = (event) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const enabled = options.filter((o) => !o.disabled);
    const index = enabled.findIndex((o) => o.value === value);
    let next = index;
    if (event.key === "ArrowLeft") next = Math.max(0, index - 1);
    if (event.key === "ArrowRight") next = Math.min(enabled.length - 1, index + 1);
    if (event.key === "Home") next = 0;
    if (event.key === "End") next = enabled.length - 1;
    const option = enabled[next];
    if (option && option.value !== value) {
      onChange(option.value);
      requestAnimationFrame(() => track.current?.querySelector('[aria-checked="true"]')?.focus());
    }
  };

  return (
    <div
      ref={track}
      role="radiogroup"
      aria-label={ariaLabel}
      className={`seg seg-${size} ${fill ? "seg-fill" : ""} ${ready ? "seg-ready" : ""} ${className}`.replace(/\s+/g, " ").trim()}
      onKeyDown={onKeyDown}
    >
      {thumb && <span className="seg-thumb" style={{ transform: `translateX(${thumb.left}px)`, width: thumb.width }} aria-hidden="true" />}
      {options.map((option) => {
        const on = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            title={option.title}
            disabled={option.disabled}
            className={on ? "on" : ""}
            onClick={() => !on && onChange(option.value)}
          >
            {option.icon && <Icon name={option.icon} size={size === "sm" ? 12 : 14} />}
            {option.label != null && <span>{option.label}</span>}
            {option.count != null && <em>{option.count}</em>}
          </button>
        );
      })}
    </div>
  );
}
