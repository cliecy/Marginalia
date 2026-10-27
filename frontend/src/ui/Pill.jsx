import Icon from "./Icon.jsx";

/**
 * A rounded pill: a quiet tag, or — with onClick — a small pill button
 * (Magpie's "↑ 更新"). Tones tint it: neutral, accent, green, amber, red.
 *
 * @param {object} props
 * @param {"neutral"|"accent"|"green"|"amber"|"red"} [props.tone]
 * @param {string} [props.icon] icon name shown before the text
 * @param {Function} [props.onClick] renders a <button> when given
 */
export function Pill({ tone = "neutral", icon, children, onClick, className = "", title, disabled, ...rest }) {
  const cls = `pill pill-${tone} ${onClick ? "pill-btn" : ""} ${className}`.replace(/\s+/g, " ").trim();
  const inner = (
    <>
      {icon && <Icon name={icon} size={13} />}
      {children != null && <span>{children}</span>}
    </>
  );
  return onClick ? (
    <button type="button" className={cls} onClick={onClick} title={title} disabled={disabled} {...rest}>{inner}</button>
  ) : (
    <span className={cls} title={title} {...rest}>{inner}</span>
  );
}

/**
 * A compact badge: a count or a short uppercase label (Magpie's `.badge`).
 *
 * @param {object} props
 * @param {"neutral"|"accent"|"green"|"amber"|"red"} [props.tone]
 * @param {boolean} [props.caps] small caps label style (default false: a count)
 */
export function Badge({ tone = "neutral", caps = false, children, className = "", ...rest }) {
  return (
    <span className={`badge badge-${tone} ${caps ? "caps" : ""} ${className}`.replace(/\s+/g, " ").trim()} {...rest}>
      {children}
    </span>
  );
}

export default Pill;
