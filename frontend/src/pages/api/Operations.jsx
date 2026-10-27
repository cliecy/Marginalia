import { Fragment, useMemo, useState } from "react";
import { Button, CodeTabs, EmptyState, List, ListFoot, Row, Skeleton } from "../../ui/index.js";
import { curlForOperation } from "./snippets.js";

const FOLDED = 10;

/** GET / POST / PUT / DELETE in its colour, fixed width so paths line up. */
export function MethodBadge({ method }) {
  return <span className={`api-m api-m-${method.toLowerCase()}`}>{method === "DELETE" ? "DEL" : method}</span>;
}

/** A path with its {placeholders} dimmed. */
function PathText({ path }) {
  return (
    <code className="api-path">
      {path.split(/(\{[^}]+\})/).map((part, i) => (part.startsWith("{") ? <i key={i}>{part}</i> : part))}
    </code>
  );
}

/** name · type · where · required · description, one line per field. */
export function FieldTable({ title, items }) {
  return (
    <div className="api-fields">
      <div className="api-fields-title">{title}</div>
      {items.map((item) => (
        <div key={`${item.in || ""}:${item.name}`} className="api-field">
          <code className="api-field-name">{item.name}</code>
          {item.type && <span className="api-field-type">{item.type}</span>}
          {item.in && item.in !== "query" && <span className="api-tag">{item.in}</span>}
          {item.required && <span className="api-tag api-tag-req">必填</span>}
          {item.description && <span className="api-field-desc">{item.description}</span>}
        </div>
      ))}
    </div>
  );
}

function OperationDetail({ op }) {
  const query = op.params.filter((p) => p.in !== "header");
  return (
    <div className="api-detail">
      {op.description && op.description !== op.summary && <p className="api-desc">{op.description}</p>}
      {query.length > 0 && <FieldTable title="参数" items={query} />}
      {op.body.length > 0 && <FieldTable title={`请求体 · ${op.bodyType}`} items={op.body} />}
      {op.stream && <p className="api-desc api-desc-note">回复是 text/event-stream：每个事件一行 data: {"{…}"}，curl 加了 -N 以便边收边显示。</p>}
      <CodeTabs tabs={[{ id: "curl", label: "curl", lang: "bash", code: curlForOperation(op) }]} />
    </div>
  );
}

/** Placeholder rows while a list loads (Magpie's gateway skeleton rows). */
export function SkeletonRows({ count = 4, badge = true }) {
  return Array.from({ length: count }, (_, i) => (
    <div key={i} className="row compact api-sk" aria-hidden="true">
      {badge && <Skeleton width={38} height={16} radius={5} />}
      <div className="who">
        <Skeleton width={`${28 + ((i * 17) % 34)}%`} height={10} />
        <Skeleton width={`${46 + ((i * 23) % 30)}%`} height={7} style={{ marginTop: 7 }} />
      </div>
    </div>
  ));
}

const matches = (op, q) => `${op.method} ${op.path} ${op.summary} ${op.tag} ${op.description}`.toLowerCase().includes(q);

/**
 * The REST endpoints from the OpenAPI document: method badge, path, summary;
 * a row opens to show its parameters and a ready-made curl call.
 *
 * @param {object} props
 * @param {ReturnType<import("../../lib/appApi.js").openApiOperations>} props.ops
 * @param {boolean} props.loading
 * @param {Error|null} props.error
 * @param {() => void} props.onRetry
 * @param {string} props.query lower-cased filter text
 */
export default function Operations({ ops, loading, error, onRetry, query }) {
  const [open, setOpen] = useState(null);
  const [all, setAll] = useState(false);
  const q = query.trim().toLowerCase();
  const filtered = useMemo(() => (q ? ops.filter((op) => matches(op, q)) : ops), [ops, q]);
  const grouped = new Set(ops.map((op) => op.tag)).size > 1;
  const shown = q || all ? filtered : filtered.slice(0, FOLDED);
  const rest = filtered.length - shown.length;

  if (error) {
    return (
      <List>
        <EmptyState compact icon="alert-circle" title="读不到 OpenAPI 文档" actions={<Button size="sm" icon="refresh" onClick={onRetry}>重试</Button>}>
          {error.message}
        </EmptyState>
      </List>
    );
  }
  if (loading && !ops.length) return <List><SkeletonRows count={5} /></List>;
  if (!filtered.length) {
    return (
      <List>
        <EmptyState compact icon="search" title={q ? "没有匹配的接口" : "文档里没有接口"}>
          {q ? `换个关键词试试，比如 note、search、chat。` : null}
        </EmptyState>
      </List>
    );
  }

  let lastTag = null;
  return (
    <List className="api-list">
      {shown.map((op) => {
        const heading = grouped && op.tag !== lastTag ? op.tag || "其他" : null;
        lastTag = op.tag;
        const isOpen = open === op.id;
        return (
          <Fragment key={op.id}>
            {heading && <div className="api-group">{heading}</div>}
            <Row
              compact
              className={`api-op ${isOpen ? "api-open" : ""}`.trim()}
              icon={<MethodBadge method={op.method} />}
              title={<PathText path={op.path} />}
              sub={op.summary || op.description.split("\n")[0] || null}
              meta={op.stream ? <span className="api-tag">SSE</span> : null}
              chevron
              aria-expanded={isOpen}
              onClick={() => setOpen(isOpen ? null : op.id)}
            />
            {isOpen && <OperationDetail op={op} />}
          </Fragment>
        );
      })}
      {rest > 0 && <ListFoot onClick={() => setAll(true)}>显示其余 {rest} 个接口</ListFoot>}
      {all && !q && filtered.length > FOLDED && <ListFoot onClick={() => setAll(false)}>收起</ListFoot>}
    </List>
  );
}
