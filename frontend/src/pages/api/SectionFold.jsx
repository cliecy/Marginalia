import { Icon } from "../../ui/index.js";

/**
 * A section heading that folds its section away (Magpie's `.row-head` with a
 * `.fold` button on the gateway page): a faint chevron, the label, a count,
 * a faint note, and actions at the right.
 *
 * @param {object} props
 * @param {React.ReactNode} props.title
 * @param {boolean} props.open
 * @param {(open: boolean) => void} props.onToggle
 * @param {React.ReactNode} [props.count] tabular number after the title
 * @param {React.ReactNode} [props.note]  faint text beside it
 * @param {React.ReactNode} [props.children] right-aligned actions (shown while open)
 * @param {string} [props.controls] id of the folded region (aria-controls)
 */
export default function SectionFold({ title, open, onToggle, count, note, children, controls }) {
  return (
    <div className="api-head">
      <button type="button" className="api-fold" aria-expanded={open} aria-controls={controls} onClick={() => onToggle(!open)}>
        <Icon name="chevron-right" size={12} stroke={1.8} />
        <span className="api-fold-title">{title}</span>
        {count != null && <em className="tabnum">{count}</em>}
      </button>
      {note != null && <span className="api-head-note">{note}</span>}
      {open && children && <div className="api-head-actions">{children}</div>}
    </div>
  );
}
