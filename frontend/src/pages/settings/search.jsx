import { createContext, useContext } from "react";
import { useApp } from "../../app/AppContext.jsx";
import { Icon, Row, SectionHead } from "../../ui/index.js";
import { envOverride } from "./env.js";

/**
 * Settings search (Magpie's 查找设置 field in the top bar). While a query is
 * set, every section renders at once and each row hides itself unless its
 * words match; CSS then hides the sections and lists left empty.
 */
export const SettingsQuery = createContext("");

/** The current settings query, lower-cased and trimmed ("" when not searching). */
export const useSettingsQuery = () => useContext(SettingsQuery);

const words = (node) => (typeof node === "string" || typeof node === "number" ? String(node) : "");

/** True when every term of the query occurs in the given texts. */
export function settingMatches(query, ...texts) {
  if (!query) return true;
  const haystack = texts.map(words).join(" ").toLowerCase();
  return query.split(/\s+/).every((term) => haystack.includes(term));
}

/**
 * A quiet note that an environment variable decides this setting at start-up.
 *
 * @param {object} props
 * @param {string} props.name the NOTES_* variable
 */
export function EnvNote({ name }) {
  return (
    <span className="set-env" title={`启动时以环境变量 ${name} 为准：这里的修改立即生效，但下次启动会被它覆盖。`}>
      <Icon name="lock" size={11} stroke={1.7} />
      由 <code>{name}</code> 决定
    </span>
  );
}

/**
 * One setting: the kit's Row (label, description, control at the right),
 * hidden while searching unless its title, description or keywords match.
 *
 * @param {object} props  Row props, plus:
 * @param {string} [props.keywords] extra words that should find this row
 * @param {string} [props.env] the setting's key in ENV_VARS: notes an environment override under the description
 */
export function SetRow({ keywords = "", env, title, sub, className = "", ...rest }) {
  const query = useSettingsQuery();
  const ctx = useApp();
  const locked = env ? envOverride(ctx, env) : null;
  if (query && !settingMatches(query, title, sub, keywords, locked)) return null;
  const line = locked ? (
    <>
      {sub}
      {sub != null && sub !== "" && <span className="set-env-sep"> · </span>}
      <EnvNote name={locked} />
    </>
  ) : (
    sub
  );
  return <Row title={title} sub={line} className={`set-row ${className}`.trim()} {...rest} />;
}

/** A block that is not a row (a form card, a hero): shown while searching only when its keywords match. */
export function SetBlock({ keywords = "", className = "", children }) {
  const query = useSettingsQuery();
  if (query && !settingMatches(query, keywords)) return null;
  return <div className={`set-block ${className}`.trim()}>{children}</div>;
}

/** A small heading inside a section; hidden while searching (the section label stands in). */
export function SetHead(props) {
  return (
    <div className="set-head">
      <SectionHead {...props} />
    </div>
  );
}
