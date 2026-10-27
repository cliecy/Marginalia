import { useMemo } from "react";
import hljs from "highlight.js/lib/common";
import { maskIn } from "../../lib/appApi.js";
import { CopyButton, Segmented } from "../../ui/index.js";

function highlight(code, lang) {
  if (!lang || !hljs.getLanguage(lang)) return null;
  try {
    return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
  } catch {
    return null;
  }
}

/**
 * Example snippets behind a segmented switch — the kit's CodeTabs, plus one
 * thing it cannot do: a secret inside the code is masked on screen while the
 * copy button copies the real text (Magpie's 示例 block on the gateway page).
 *
 * @param {object} props
 * @param {Array<{id: string, label: string, code: string, lang?: string, note?: React.ReactNode}>} props.tabs
 * @param {string} props.value  the shown tab's id (falls back to the first tab)
 * @param {(id: string) => void} props.onChange
 * @param {string} [props.secret] text to mask wherever it appears
 */
export default function Snippets({ tabs, value, onChange, secret = "" }) {
  const current = tabs.find((t) => t.id === value) ?? tabs[0];
  const shown = current ? maskIn(current.code, secret) : "";
  const html = useMemo(() => (current ? highlight(shown, current.lang) : null), [current, shown]);
  if (!current) return null;
  const masked = !!secret && current.code.includes(secret);
  return (
    <div className="code-tabs api-snippets">
      {tabs.length > 1 && <Segmented size="sm" value={current.id} onChange={onChange} options={tabs.map((t) => ({ value: t.id, label: t.label }))} ariaLabel="示例" />}
      <div className="snip-wrap">
        <pre className="snip">{html != null ? <code className="hljs" dangerouslySetInnerHTML={{ __html: html }} /> : <code>{shown}</code>}</pre>
        <CopyButton text={current.code} label={masked ? "复制（含完整令牌）" : "复制"} className="snip-copy" />
      </div>
      {(current.note || masked) && (
        <div className="code-note">
          {current.note}
          {masked && <span className="api-masked-note">{current.note ? " " : ""}令牌在这里显示为 ••••，复制时会带上完整令牌。</span>}
        </div>
      )}
    </div>
  );
}
