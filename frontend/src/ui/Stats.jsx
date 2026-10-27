/**
 * A row of key figures in one card, cells divided by hairlines (Magpie's
 * `.stats` with `.kpi` cells): the number large and tabular, its label in
 * small caps under it, an optional hint line.
 *
 * @param {object} props
 * @param {Array<{label: React.ReactNode, value: React.ReactNode, unit?: string, hint?: React.ReactNode, tone?: "green"|"amber"|"red"|"accent", onClick?: Function}>} props.items
 * @param {number} [props.columns] defaults to the number of items (max 6)
 * @param {boolean} [props.loading] draws skeletons in place of the numbers
 */
export default function Stats({ items, columns, loading = false, className = "" }) {
  const cols = columns ?? Math.min(Math.max(items.length, 1), 6);
  return (
    <div className={`stats ${className}`.trim()} style={{ "--stats-cols": cols }}>
      {items.map((item, index) => {
        const Tag = item.onClick ? "button" : "div";
        return (
          <Tag key={index} type={item.onClick ? "button" : undefined} className={`kpi ${item.tone ? `kpi-${item.tone}` : ""} ${item.onClick ? "kpi-btn" : ""}`.trim()} onClick={item.onClick}>
            {loading ? (
              <i className="skeleton kpi-skel" />
            ) : (
              <b>
                {item.value}
                {item.unit && <em className="u">{item.unit}</em>}
              </b>
            )}
            <span>{item.label}</span>
            {item.hint != null && <small>{item.hint}</small>}
          </Tag>
        );
      })}
    </div>
  );
}
