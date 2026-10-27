import Icon from "./Icon.jsx";

/**
 * A card of rows: white, radius 12, a 1px hairline, rows divided by a lighter
 * line (Magpie's `.list`).
 */
export function List({ children, className = "", ...rest }) {
  return (
    <div className={`list ${className}`.trim()} {...rest}>
      {children}
    </div>
  );
}

/**
 * A list row: icon · name (500) over a muted sub line · free middle · meta ·
 * right-hand controls · chevron. Clickable rows are keyboard reachable; their
 * controls (buttons, toggles) stop the row's click.
 *
 * @param {object} props
 * @param {string|React.ReactNode} [props.icon] icon name, or any node (a logo, a StatusDot)
 * @param {React.ReactNode} props.title
 * @param {React.ReactNode} [props.sub]
 * @param {React.ReactNode} [props.meta]  right-aligned muted text (time, counts)
 * @param {React.ReactNode} [props.right] right-aligned controls
 * @param {boolean} [props.chevron] trailing chevron (for rows that open something)
 * @param {Function} [props.onClick]
 * @param {boolean} [props.active] selected look
 * @param {boolean} [props.compact] 40px min height instead of 48
 * @param {React.ReactNode} [props.children] extra content between the text and meta
 */
export function Row({ icon, title, sub, meta, right, chevron = false, onClick, active = false, compact = false, disabled = false, className = "", children, ...rest }) {
  const clickable = !!onClick && !disabled;
  const onKeyDown = (event) => {
    if (!clickable || event.target !== event.currentTarget) return;
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onClick(event);
    }
  };
  const stop = (event) => event.stopPropagation();
  return (
    <div
      className={`row ${clickable ? "clickable" : ""} ${active ? "active" : ""} ${compact ? "compact" : ""} ${disabled ? "disabled" : ""} ${className}`
        .replace(/\s+/g, " ")
        .trim()}
      role={clickable ? "button" : undefined}
      tabIndex={clickable ? 0 : undefined}
      aria-pressed={clickable && active ? true : undefined}
      onClick={clickable ? onClick : undefined}
      onKeyDown={onKeyDown}
      {...rest}
    >
      {icon != null && <span className="row-ic">{typeof icon === "string" ? <Icon name={icon} size={16} /> : icon}</span>}
      <div className="who">
        <div className="name">{title}</div>
        {sub != null && sub !== "" && <div className="sub">{sub}</div>}
      </div>
      {children}
      {meta != null && <div className="meta">{meta}</div>}
      {right != null && (
        <div className="row-right" onClick={stop} onKeyDown={stop}>
          {right}
        </div>
      )}
      {chevron && <Icon name="chevron-right" size={14} className="row-chev" />}
    </div>
  );
}

/** The fold/more line at the foot of a list ("显示其余 10 个"). */
export function ListFoot({ children, onClick, className = "" }) {
  return onClick ? (
    <button type="button" className={`list-foot ${className}`.trim()} onClick={onClick}>
      {children}
    </button>
  ) : (
    <div className={`list-foot ${className}`.trim()}>{children}</div>
  );
}

/**
 * A section heading above a list: a small muted label, a faint note, and
 * actions on the right (Magpie's `.row-head`).
 *
 * @param {object} props
 * @param {React.ReactNode} props.title
 * @param {React.ReactNode} [props.note]
 * @param {React.ReactNode} [props.children] right-aligned actions
 */
export function SectionHead({ title, note, children, className = "" }) {
  return (
    <div className={`section-head ${className}`.trim()}>
      <span className="section-title">{title}</span>
      {note != null && <span className="section-note">{note}</span>}
      {children && <div className="section-actions">{children}</div>}
    </div>
  );
}

export default List;
