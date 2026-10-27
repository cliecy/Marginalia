import { Fragment, useMemo, useState } from "react";
import { Button, CodeTabs, EmptyState, Icon, List, ListFoot, Row } from "../../ui/index.js";
import { FieldTable, SkeletonRows } from "./Operations.jsx";
import { toolCallExample } from "./snippets.js";

const FOLDED = 8;

/** What a tool may do, from its MCP annotations: read, write, or delete. */
export function toolKind(tool) {
  if (tool.annotations?.destructiveHint) return "delete";
  if (tool.annotations?.readOnlyHint) return "read";
  return "write";
}

const KINDS = {
  read: { icon: "eye", label: "只读", tone: "" },
  write: { icon: "edit", label: "写入", tone: "api-tag-amber" },
  delete: { icon: "trash", label: "删除", tone: "api-tag-red" },
};

/** The first sentence of a description (up to the first "。" or line break). */
function firstSentence(text) {
  const line = (text || "").split("\n")[0];
  const end = line.indexOf("。");
  return end >= 0 ? line.slice(0, end + 1) : line;
}

/** The tool's arguments as table rows. */
function argumentsOf(tool) {
  const schema = tool.inputSchema || {};
  const required = new Set(schema.required || []);
  return Object.entries(schema.properties || {}).map(([name, prop]) => ({
    name,
    type: Array.isArray(prop?.type) ? prop.type.join(" | ") : prop?.type === "array" ? `${prop.items?.type || "any"}[]` : prop?.type || "",
    required: required.has(name),
    description: prop?.description || "",
  }));
}

function ToolDetail({ tool }) {
  const args = argumentsOf(tool);
  return (
    <div className="api-detail">
      {tool.description && <p className="api-desc">{tool.description}</p>}
      {args.length > 0 ? <FieldTable title="参数" items={args} /> : <p className="api-desc api-desc-note">这个工具不需要参数。</p>}
      <CodeTabs tabs={[{ id: "rpc", label: "tools/call", lang: "json", code: toolCallExample(tool) }]} />
    </div>
  );
}

/**
 * The MCP tools the knowledge base offers (a live tools/list): name, what it
 * does, whether it reads, writes or deletes. A row opens to its arguments and
 * a tools/call example.
 *
 * @param {object} props
 * @param {Array<object>} props.tools
 * @param {boolean} props.loading
 * @param {Error|null} props.error
 * @param {() => void} props.onRetry
 * @param {string} props.query
 */
export default function Tools({ tools, loading, error, onRetry, query }) {
  const [open, setOpen] = useState(null);
  const [all, setAll] = useState(false);
  const q = query.trim().toLowerCase();
  const filtered = useMemo(
    () => (q ? tools.filter((t) => `${t.name} ${t.title || ""} ${t.description || ""}`.toLowerCase().includes(q)) : tools),
    [tools, q],
  );
  const shown = q || all ? filtered : filtered.slice(0, FOLDED);
  const rest = filtered.length - shown.length;

  if (error) {
    return (
      <List>
        <EmptyState compact icon="alert-circle" title="读不到 MCP 工具" actions={<Button size="sm" icon="refresh" onClick={onRetry}>重试</Button>}>
          {error.message}
        </EmptyState>
      </List>
    );
  }
  if (loading && !tools.length) return <List><SkeletonRows count={4} badge={false} /></List>;
  if (!filtered.length) {
    return (
      <List>
        <EmptyState compact icon={q ? "search" : "plug"} title={q ? "没有匹配的工具" : "MCP 服务没有提供工具"} />
      </List>
    );
  }

  return (
    <List className="api-list">
      {shown.map((tool) => {
        const kind = KINDS[toolKind(tool)];
        const isOpen = open === tool.name;
        return (
          <Fragment key={tool.name}>
            <Row
              compact
              className={`api-tool ${isOpen ? "api-open" : ""}`.trim()}
              icon={<Icon name={kind.icon} size={14} />}
              title={<code className="api-path">{tool.name}</code>}
              sub={firstSentence(tool.description)}
              meta={<span className={`api-tag ${kind.tone}`.trim()}>{kind.label}</span>}
              chevron
              aria-expanded={isOpen}
              onClick={() => setOpen(isOpen ? null : tool.name)}
            />
            {isOpen && <ToolDetail tool={tool} />}
          </Fragment>
        );
      })}
      {rest > 0 && <ListFoot onClick={() => setAll(true)}>显示其余 {rest} 个工具</ListFoot>}
      {all && !q && filtered.length > FOLDED && <ListFoot onClick={() => setAll(false)}>收起</ListFoot>}
    </List>
  );
}
