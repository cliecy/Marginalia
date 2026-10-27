/**
 * A 7px status dot with a soft 3px ring, as Magpie's gateway line has.
 *
 * @param {object} props
 * @param {"green"|"grey"|"amber"|"red"|"accent"} [props.tone] default "green"
 * @param {boolean} [props.ring]  draw the soft ring (default true)
 * @param {boolean} [props.pulse] breathe gently (for "working…")
 * @param {string}  [props.label] accessible label; decorative without one
 */
export default function StatusDot({ tone = "green", ring = true, pulse = false, label, className = "" }) {
  return (
    <i
      className={`sdot sdot-${tone} ${ring ? "ring" : ""} ${pulse ? "pulse" : ""} ${className}`.replace(/\s+/g, " ").trim()}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    />
  );
}
