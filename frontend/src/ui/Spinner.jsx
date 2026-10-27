/**
 * A small activity indicator: a quarter arc turning in the text colour.
 *
 * @param {object} props
 * @param {number} [props.size]  diameter in px (default 14)
 * @param {string} [props.label] accessible label (default "加载中")
 */
export default function Spinner({ size = 14, label = "加载中", className = "" }) {
  return (
    <span className={`spinner ${className}`.trim()} role="status" aria-label={label} style={{ width: size, height: size }}>
      <svg viewBox="0 0 16 16" width={size} height={size} aria-hidden="true">
        <circle cx="8" cy="8" r="6.25" fill="none" stroke="currentColor" strokeOpacity=".22" strokeWidth="1.6" />
        <path d="M8 1.75A6.25 6.25 0 0 1 14.25 8" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
      </svg>
    </span>
  );
}
