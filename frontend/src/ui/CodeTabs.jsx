import { useMemo, useState } from "react";
import hljs from "highlight.js/lib/common";
import Segmented from "./Segmented.jsx";
import { CopyButton } from "./CopyField.jsx";

function highlight(code, lang) {
  if (!lang) return null;
  try {
    if (!hljs.getLanguage(lang)) return null;
    return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
  } catch {
    return null;
  }
}

/**
 * Example snippets behind a segmented switch, each with a copy button
 * (Magpie's 示例 block: Shell · curl · Python · Node …).
 *
 * @param {object} props
 * @param {Array<{id: string, label: string, code: string, lang?: string, note?: React.ReactNode}>} props.tabs
 *        lang is a highlight.js language: "bash", "python", "javascript", "json", "ini" (TOML)…
 * @param {string} [props.value]  controlled tab id
 * @param {(id: string) => void} [props.onChange]
 */
export default function CodeTabs({ tabs, value, onChange, className = "" }) {
  const [own, setOwn] = useState(tabs[0]?.id);
  const current = tabs.find((t) => t.id === (value ?? own)) ?? tabs[0];
  const html = useMemo(() => (current ? highlight(current.code, current.lang) : null), [current]);
  if (!current) return null;
  const pick = (id) => (onChange ? onChange(id) : setOwn(id));
  return (
    <div className={`code-tabs ${className}`.trim()}>
      {tabs.length > 1 && <Segmented size="sm" value={current.id} onChange={pick} options={tabs.map((t) => ({ value: t.id, label: t.label }))} />}
      <div className="snip-wrap">
        {html != null ? (
          <pre className="snip">
            <code className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
          </pre>
        ) : (
          <pre className="snip">
            <code>{current.code}</code>
          </pre>
        )}
        <CopyButton text={current.code} className="snip-copy" />
      </div>
      {current.note && <div className="code-note">{current.note}</div>}
    </div>
  );
}
