import { useMemo } from "react";
import { diffLines } from "diff";

/** Splits a diff part's text into its lines (a trailing newline ends the last line, it does not start a new one). */
const linesOf = (value) => value.replace(/\n$/, "").split("\n");

/**
 * Counts the lines a change adds and removes.
 *
 * @param {string} before
 * @param {string} after
 * @returns {{added: number, removed: number}}
 */
export function diffStats(before = "", after = "") {
  let added = 0;
  let removed = 0;
  for (const part of diffLines(before || "", after || "")) {
    if (part.added) added += linesOf(part.value).length;
    else if (part.removed) removed += linesOf(part.value).length;
  }
  return { added, removed };
}

/**
 * Builds the rows of a line diff: unchanged runs longer than `context * 2 + 1`
 * lines fold to `context` lines each side and a "skip" row. Every row carries
 * its old/new line numbers.
 */
function buildRows(before, after, context) {
  const rows = [];
  let oldNo = 1;
  let newNo = 1;
  for (const part of diffLines(before, after)) {
    const lines = linesOf(part.value);
    if (part.added) {
      for (const text of lines) rows.push({ kind: "add", text, newNo: newNo++ });
    } else if (part.removed) {
      for (const text of lines) rows.push({ kind: "del", text, oldNo: oldNo++ });
    } else if (lines.length > context * 2 + 1) {
      for (const text of lines.slice(0, context)) rows.push({ kind: "same", text, oldNo: oldNo++, newNo: newNo++ });
      const hidden = lines.length - context * 2;
      rows.push({ kind: "skip", text: `${hidden} 行未改动` });
      oldNo += hidden;
      newNo += hidden;
      for (const text of lines.slice(-context)) rows.push({ kind: "same", text, oldNo: oldNo++, newNo: newNo++ });
    } else {
      for (const text of lines) rows.push({ kind: "same", text, oldNo: oldNo++, newNo: newNo++ });
    }
  }
  return rows;
}

const SIGN = { add: "+", del: "−", same: " " };

/**
 * A line diff in Magpie's quiet style: monospace rows, removed lines in red and
 * added ones in green over a faint tint, old/new line numbers in a gutter, and
 * unchanged stretches folded to a few lines of context.
 *
 * @param {object} props
 * @param {string} [props.before]
 * @param {string} [props.after]
 * @param {number} [props.context]   unchanged lines kept around each change (default 3)
 * @param {boolean} [props.numbers]  show the line-number gutter (default true)
 * @param {number|string} [props.maxHeight] scroll inside this height (default 58vh; "none" to grow)
 * @param {string} [props.className]
 */
export default function DiffView({ before = "", after = "", context = 3, numbers = true, maxHeight, className = "" }) {
  const rows = useMemo(() => buildRows(before || "", after || "", context), [before, after, context]);
  const unchanged = rows.length > 0 && rows.every((row) => row.kind === "same" || row.kind === "skip");

  return (
    <div
      className={`diff ${numbers ? "diff-numbered" : ""} ${className}`.replace(/\s+/g, " ").trim()}
      style={maxHeight !== undefined ? { maxHeight: maxHeight === "none" ? "none" : maxHeight } : undefined}
      role="group"
      aria-label="差异"
    >
      {unchanged && <div className="diff-note">内容没有变化</div>}
      {rows.map((row, i) =>
        row.kind === "skip" ? (
          <div key={i} className="diff-skip">
            <span>{row.text}</span>
          </div>
        ) : (
          <div key={i} className={`diff-row diff-${row.kind}`}>
            {numbers && (
              <>
                <span className="diff-no" aria-hidden="true">{row.oldNo ?? ""}</span>
                <span className="diff-no" aria-hidden="true">{row.newNo ?? ""}</span>
              </>
            )}
            <span className="diff-sign" aria-hidden="true">{SIGN[row.kind]}</span>
            <code className="diff-text">{row.text || " "}</code>
          </div>
        ),
      )}
    </div>
  );
}
