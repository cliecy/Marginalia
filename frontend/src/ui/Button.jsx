import { forwardRef } from "react";
import Icon from "./Icon.jsx";
import Spinner from "./Spinner.jsx";

/**
 * A text button in one of Magpie's looks.
 *
 * - "default": a bordered card button (27px) — the everyday action
 * - "primary": accent fill — the one action a view is for
 * - "soft":    accent-soft pill — a page's primary action in the top bar
 * - "ghost":   quiet text that gets a pill on hover
 * - "danger":  ghost that turns red on hover; "danger-fill" for confirm dialogs
 *
 * @param {object} props
 * @param {"default"|"primary"|"soft"|"ghost"|"danger"|"danger-fill"} [props.variant]
 * @param {"sm"|"md"|"lg"} [props.size]
 * @param {string} [props.icon] icon before the label
 * @param {string} [props.iconRight] icon after the label
 * @param {boolean} [props.loading] shows a spinner and disables the button
 */
const Button = forwardRef(function Button(
  { variant = "default", size = "md", icon, iconRight, loading = false, disabled, children, className = "", type = "button", ...rest },
  ref,
) {
  const cls = `btn btn-${variant} btn-${size} ${loading ? "is-loading" : ""} ${!children ? "btn-icon-only" : ""} ${className}`
    .replace(/\s+/g, " ")
    .trim();
  return (
    <button ref={ref} type={type} className={cls} disabled={disabled || loading} aria-busy={loading || undefined} {...rest}>
      {loading ? <Spinner size={13} /> : icon ? <Icon name={icon} size={size === "sm" ? 13 : 14} /> : null}
      {children != null && children !== false && <span className="btn-label">{children}</span>}
      {iconRight && <Icon name={iconRight} size={size === "sm" ? 12 : 13} className="btn-icon-right" />}
    </button>
  );
});

/**
 * A square icon-only button (26px, radius 7): muted, a pill on hover, "on"
 * while its thing is showing. `label` is its tooltip and accessible name.
 *
 * @param {object} props
 * @param {string} props.icon
 * @param {string} props.label
 * @param {boolean} [props.active]
 * @param {"sm"|"md"} [props.size]
 */
export const IconButton = forwardRef(function IconButton(
  { icon, label, active = false, size = "md", className = "", type = "button", iconSize, children, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={`icon-btn icon-btn-${size} ${active ? "on" : ""} ${className}`.replace(/\s+/g, " ").trim()}
      title={label}
      aria-label={label}
      aria-pressed={active || undefined}
      {...rest}
    >
      <Icon name={icon} size={iconSize ?? (size === "sm" ? 14 : 16)} />
      {children}
    </button>
  );
});

export default Button;
