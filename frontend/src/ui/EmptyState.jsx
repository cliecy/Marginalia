import Icon from "./Icon.jsx";

/**
 * A calm centred empty state: a glyph in a soft tile, a title, a muted line,
 * and actions.
 *
 * @param {object} props
 * @param {string} [props.icon] icon name
 * @param {React.ReactNode} props.title
 * @param {React.ReactNode} [props.children] the explanation
 * @param {React.ReactNode} [props.actions] buttons under it
 * @param {boolean} [props.compact] less padding (inside panels and lists)
 */
export default function EmptyState({ icon, title, children, actions, compact = false, className = "" }) {
  return (
    <div className={`empty ${compact ? "compact" : ""} ${className}`.replace(/\s+/g, " ").trim()}>
      {icon && (
        <span className="empty-ic">
          <Icon name={icon} size={compact ? 16 : 18} />
        </span>
      )}
      <b className="empty-title">{title}</b>
      {children != null && <div className="empty-text">{children}</div>}
      {actions && <div className="empty-actions">{actions}</div>}
    </div>
  );
}
