import { forwardRef, useEffect, useId, useMemo, useRef, useState } from "react";
import Icon from "./Icon.jsx";
import { isComposingKey } from "./keys.js";
import { Popover } from "./Popover.jsx";

/**
 * A labelled form control. Stacked by default; `inline` puts the label in a
 * right-aligned 88px column beside the control, as Magpie's editors do.
 *
 * @param {object} props
 * @param {React.ReactNode} props.label
 * @param {React.ReactNode} [props.hint]  muted help under the control
 * @param {React.ReactNode} [props.error] red message under the control
 * @param {boolean} [props.inline]
 * @param {string} [props.htmlFor] id of the control (or give the control the id Field passes to a render function)
 * @param {React.ReactNode | ((id: string) => React.ReactNode)} props.children
 */
export function Field({ label, hint, error, inline = false, htmlFor, className = "", children }) {
  const autoId = useId();
  const id = htmlFor ?? autoId;
  return (
    <div className={`field ${inline ? "field-inline" : ""} ${error ? "has-error" : ""} ${className}`.replace(/\s+/g, " ").trim()}>
      {label != null && <label className="field-label" htmlFor={id}>{label}</label>}
      <div className="field-body">
        {typeof children === "function" ? children(id) : children}
        {error ? <div className="field-error" role="alert">{error}</div> : hint != null ? <div className="field-hint">{hint}</div> : null}
      </div>
    </div>
  );
}

/**
 * A text input: 27px, radius 7, hairline border, accent ring on focus.
 *
 * @param {object} props
 * @param {"sm"|"md"|"lg"} [props.size]
 * @param {boolean} [props.mono] monospace (paths, URLs, tokens)
 */
export const Input = forwardRef(function Input({ size = "md", mono = false, className = "", type = "text", ...rest }, ref) {
  return <input ref={ref} type={type} className={`input input-${size} ${mono ? "mono" : ""} ${className}`.replace(/\s+/g, " ").trim()} spellCheck={false} {...rest} />;
});

/** A multi-line input styled like Input. */
export const TextArea = forwardRef(function TextArea({ mono = false, className = "", rows = 4, ...rest }, ref) {
  return <textarea ref={ref} rows={rows} className={`input textarea ${mono ? "mono" : ""} ${className}`.replace(/\s+/g, " ").trim()} {...rest} />;
});

/**
 * A search field with a glass icon and a clear button.
 *
 * @param {object} props
 * @param {string} props.value
 * @param {(value: string) => void} props.onChange
 * @param {(value: string) => void} [props.onEnter]
 * @param {() => void} [props.onEscape] default: clears the field
 * @param {"sm"|"md"} [props.size]
 */
export const SearchInput = forwardRef(function SearchInput(
  { value, onChange, onEnter, onEscape, placeholder = "搜索", size = "md", className = "", ...rest },
  ref,
) {
  const onKeyDown = (event) => {
    if (isComposingKey(event)) return;
    if (event.key === "Enter") onEnter?.(value);
    if (event.key === "Escape") {
      if (onEscape) onEscape();
      else if (value) {
        event.stopPropagation();
        onChange("");
      }
    }
    rest.onKeyDown?.(event);
  };
  return (
    <div className={`search search-${size} ${value ? "has-value" : ""} ${className}`.replace(/\s+/g, " ").trim()}>
      <Icon name="search" size={13} className="search-ic" />
      <input
        ref={ref}
        type="text"
        value={value}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        {...rest}
        onKeyDown={onKeyDown}
        onChange={(event) => onChange(event.target.value)}
      />
      {value && (
        <button type="button" className="search-clear" aria-label="清除" tabIndex={-1} onMouseDown={(e) => e.preventDefault()} onClick={() => onChange("")}>
          <Icon name="x" size={11} stroke={1.8} />
        </button>
      )}
    </div>
  );
});

const normalize = (text) => String(text ?? "").toLowerCase();

/**
 * A pop-up button that picks one value from a list (Magpie's `.field` chip with
 * a model-picker popover): optional search, groups, a sub-line per option.
 *
 * @param {object} props
 * @param {string} props.value
 * @param {(value: string) => void} props.onChange
 * @param {Array<{value: string, label?: React.ReactNode, hint?: string, group?: string, icon?: string, disabled?: boolean, keywords?: string}>} props.options
 * @param {string} [props.placeholder]
 * @param {boolean} [props.searchable] show a filter field (default: more than 8 options)
 * @param {string} [props.searchPlaceholder]
 * @param {number|string} [props.width] width of the button
 * @param {number} [props.menuWidth] width of the popover (default 300)
 * @param {"sm"|"md"} [props.size]
 * @param {React.ReactNode} [props.prefix] muted label inside the button before the value ("模型")
 */
export function Select({
  value,
  onChange,
  options,
  placeholder = "请选择",
  searchable,
  searchPlaceholder = "搜索…",
  width,
  menuWidth = 300,
  size = "md",
  prefix,
  disabled = false,
  className = "",
  title,
  ariaLabel,
}) {
  const button = useRef(null);
  const listRef = useRef(null);
  const searchRef = useRef(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const current = options.find((o) => o.value === value);
  const canSearch = searchable ?? options.length > 8;

  const filtered = useMemo(() => {
    const q = normalize(query.trim());
    if (!q) return options;
    return options.filter((o) => normalize(`${o.value} ${typeof o.label === "string" ? o.label : ""} ${o.hint ?? ""} ${o.keywords ?? ""}`).includes(q));
  }, [options, query]);

  useEffect(() => {
    if (!open) return undefined;
    setQuery("");
    const index = Math.max(0, options.findIndex((o) => o.value === value));
    setCursor(index);
    const frame = requestAnimationFrame(() => (searchRef.current || listRef.current)?.focus({ preventScroll: true }));
    return () => cancelAnimationFrame(frame);
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!open) return;
    listRef.current?.querySelector(`[data-index="${cursor}"]`)?.scrollIntoView({ block: "nearest" });
  }, [cursor, open]);

  const pick = (option) => {
    if (!option || option.disabled) return;
    setOpen(false);
    if (option.value !== value) onChange(option.value);
    button.current?.focus();
  };

  const onKeyDown = (event) => {
    if (isComposingKey(event)) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setCursor((c) => Math.min(filtered.length - 1, c + 1));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setCursor((c) => Math.max(0, c - 1));
    } else if (event.key === "Enter") {
      event.preventDefault();
      pick(filtered[cursor]);
    }
  };

  let lastGroup = null;
  return (
    <>
      <button
        ref={button}
        type="button"
        className={`select select-${size} ${open ? "open" : ""} ${className}`.replace(/\s+/g, " ").trim()}
        style={width ? { width } : undefined}
        disabled={disabled}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={ariaLabel}
        title={title ?? (typeof current?.label === "string" ? current.label : current?.value)}
        onClick={() => setOpen(!open)}
        onKeyDown={(event) => {
          if (!open && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
            event.preventDefault();
            setOpen(true);
          }
        }}
      >
        {prefix && <span className="select-k">{prefix}</span>}
        {current?.icon && <Icon name={current.icon} size={14} />}
        <span className={`select-v ${current ? "" : "placeholder"}`.trim()}>{current ? current.label ?? current.value : placeholder}</span>
        <Icon name="chevrons-up-down" size={12} className="select-chev" />
      </button>
      <Popover open={open} anchor={button.current} onClose={() => setOpen(false)} placement="bottom-start" width={menuWidth} className="pop-list" role="presentation">
        {canSearch && (
          <div className="pop-search">
            <Icon name="search" size={13} />
            <input ref={searchRef} value={query} placeholder={searchPlaceholder} onChange={(e) => (setQuery(e.target.value), setCursor(0))} onKeyDown={onKeyDown} spellCheck={false} />
          </div>
        )}
        <ul ref={listRef} role="listbox" tabIndex={canSearch ? -1 : 0} onKeyDown={canSearch ? undefined : onKeyDown}>
          {filtered.length === 0 && <li className="pop-none">没有匹配的选项</li>}
          {filtered.map((option, index) => {
            const heading = option.group && option.group !== lastGroup ? option.group : null;
            lastGroup = option.group ?? lastGroup;
            return [
              heading && (
                <li key={`g-${heading}`} className="pop-group" role="presentation">
                  {heading}
                </li>
              ),
              <li
                key={option.value}
                data-index={index}
                role="option"
                aria-selected={option.value === value}
                aria-disabled={option.disabled || undefined}
                className={`${index === cursor ? "sel" : ""} ${option.value === value ? "cur" : ""} ${option.disabled ? "disabled" : ""}`.replace(/\s+/g, " ").trim()}
                onMouseEnter={() => setCursor(index)}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => pick(option)}
              >
                {option.icon && <Icon name={option.icon} size={15} />}
                <span className="opt-words">
                  <span className="opt-v">{option.label ?? option.value}</span>
                  {option.hint && <span className="opt-n">{option.hint}</span>}
                </span>
                <Icon name="check" size={14} className="opt-check" />
              </li>,
            ];
          })}
        </ul>
      </Popover>
    </>
  );
}

export default Field;
