/**
 * An iOS-style switch (32×19). On, it is graphite in light mode and paper in
 * dark, as Magpie's are; the accent stays for actions.
 *
 * @param {object} props
 * @param {boolean} props.checked
 * @param {(next: boolean) => void} props.onChange
 * @param {boolean} [props.disabled]
 * @param {string} [props.label] accessible name (use when no visible label points at it)
 * @param {"sm"|"md"} [props.size]
 */
export default function Toggle({ checked, onChange, disabled = false, label, size = "md", className = "", id }) {
  return (
    <button
      id={id}
      type="button"
      role="switch"
      aria-checked={!!checked}
      aria-label={label}
      disabled={disabled}
      className={`toggle toggle-${size} ${checked ? "on" : ""} ${className}`.replace(/\s+/g, " ").trim()}
      onClick={() => onChange?.(!checked)}
    >
      <i />
    </button>
  );
}
