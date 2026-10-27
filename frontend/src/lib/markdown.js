import MarkdownIt from "markdown-it";
import texmath from "markdown-it-texmath";
import katex from "katex";
import hljs from "highlight.js/lib/common";
import DOMPurify from "dompurify";
import { parse as parseYaml } from "yaml";
import { API_BASE, fileUrl } from "./api.js";

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|bmp|avif)$/i;
const MEDIA_EXT = { audio: /\.(mp3|wav|ogg|m4a|flac)$/i, video: /\.(mp4|webm|mov)$/i };

/** Obsidian-style link resolution against the vault file list. */
export class LinkResolver {
  constructor(files = []) {
    this.files = new Set(files);
    this.byName = new Map();
    for (const path of files) {
      const name = path.split("/").pop();
      const keys = [name.toLowerCase()];
      if (/\.md$/i.test(name)) keys.push(name.replace(/\.md$/i, "").toLowerCase());
      for (const key of keys) {
        if (!this.byName.has(key)) this.byName.set(key, []);
        this.byName.get(key).push(path);
      }
    }
    for (const list of this.byName.values()) list.sort((a, b) => a.split("/").length - b.split("/").length || a.localeCompare(b));
  }

  resolve(target, from = "") {
    if (!target) return null;
    try {
      target = decodeURIComponent(target.trim());
    } catch {
      target = target.trim();
    }
    const dir = from.includes("/") ? from.slice(0, from.lastIndexOf("/")) : "";
    for (const candidate of [target, `${target}.md`]) {
      if (this.files.has(candidate)) return candidate;
      const relative = normalize(dir ? `${dir}/${candidate}` : candidate);
      if (this.files.has(relative)) return relative;
    }
    const name = target.split("/").pop().toLowerCase();
    return this.byName.get(name)?.[0] ?? null;
  }
}

function normalize(path) {
  const out = [];
  for (const part of path.split("/")) {
    if (part === "..") out.pop();
    else if (part && part !== ".") out.push(part);
  }
  return out.join("/");
}

const escapeHtml = (s) => s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

function wikilinkPlugin(md) {
  md.inline.ruler.before("link", "wikilink", (state, silent) => {
    const src = state.src;
    let pos = state.pos;
    const embed = src.charCodeAt(pos) === 0x21; // !
    if (embed) pos += 1;
    if (!src.startsWith("[[", pos)) return false;
    const end = src.indexOf("]]", pos + 2);
    if (end < 0 || src.slice(pos + 2, end).includes("\n")) return false;
    if (!silent) {
      const inner = src.slice(pos + 2, end);
      const [targetPart, alias] = inner.split("|");
      const hashIndex = targetPart.search(/[#^]/);
      const token = state.push(embed ? "wikiembed" : "wikilink", "", 0);
      token.meta = {
        target: hashIndex >= 0 ? targetPart.slice(0, hashIndex) : targetPart,
        anchor: hashIndex >= 0 ? targetPart.slice(hashIndex + 1) : "",
        alias: alias?.trim(),
      };
    }
    state.pos = end + 2;
    return true;
  });

  md.renderer.rules.wikilink = (tokens, idx, _, env) => {
    const { target, anchor, alias } = tokens[idx].meta;
    const resolved = target ? env.resolver?.resolve(target, env.path) : env.path;
    const label = escapeHtml(alias || (target || "").trim() + (anchor ? ` › ${anchor}` : ""));
    // An href (and the button role) makes the link reachable from the keyboard; the click handlers stop it navigating.
    if (!resolved) return `<a class="wikilink missing" href="#" role="button" data-create="${escapeHtml(target.trim())}" title="点击创建">${label}</a>`;
    return `<a class="wikilink" data-note="${escapeHtml(resolved)}" data-anchor="${escapeHtml(anchor)}" href="#">${label}</a>`;
  };

  md.renderer.rules.wikiembed = (tokens, idx, _, env) => {
    const { target, alias } = tokens[idx].meta;
    const resolved = env.resolver?.resolve(target, env.path);
    if (!resolved) return `<span class="wikilink missing">![[${escapeHtml(target)}]]</span>`;
    const url = escapeHtml(fileUrl(resolved));
    const width = alias && /^\d+(x\d+)?$/.test(alias) ? ` width="${alias.split("x")[0]}"` : "";
    if (IMAGE_EXT.test(resolved)) return `<img src="${url}" alt="${escapeHtml(target)}"${width} loading="lazy">`;
    if (/\.pdf$/i.test(resolved)) return `<iframe class="pdf-embed" src="${url}" title="${escapeHtml(resolved)}"></iframe>`;
    if (MEDIA_EXT.audio.test(resolved)) return `<audio controls src="${url}"></audio>`;
    if (MEDIA_EXT.video.test(resolved)) return `<video controls src="${url}"></video>`;
    return `<a class="wikilink embed-link" data-note="${escapeHtml(resolved)}" href="#">📄 ${escapeHtml(alias || target)}</a>`;
  };
}

function localLinksPlugin(md) {
  const defaultImage = md.renderer.rules.image;
  md.renderer.rules.image = (tokens, idx, options, env, self) => {
    const token = tokens[idx];
    const src = token.attrGet("src") || "";
    if (!/^(https?:|data:|\/)/.test(src)) {
      const resolved = env.resolver?.resolve(src, env.path);
      if (resolved) token.attrSet("src", fileUrl(resolved));
    }
    token.attrSet("loading", "lazy");
    return defaultImage(tokens, idx, options, env, self);
  };
  const defaultLinkOpen = md.renderer.rules.link_open || ((t, i, o, e, s) => s.renderToken(t, i, o));
  md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
    const token = tokens[idx];
    const href = token.attrGet("href") || "";
    if (/^(https?:|mailto:)/.test(href)) {
      token.attrSet("target", "_blank");
      token.attrSet("rel", "noopener noreferrer");
    } else if (isLocalHref(href)) {
      const target = href.split("#")[0];
      const resolved = target ? env.resolver?.resolve(target, env.path) : null;
      if (resolved) {
        token.attrSet("data-note", resolved);
        token.attrSet("href", "#");
      } else {
        // Nothing in the vault answers it: followed, it would load the app's own
        // URL space (and reload the app). Show it as a missing link instead —
        // one that creates the note when it names a note.
        missingLink(token, target, env.path);
      }
    }
    return defaultLinkOpen(tokens, idx, options, env, self);
  };
}

/**
 * True for an href that points into the app's own URL space: no scheme, not
 * protocol-relative, not an in-page #anchor. Such a link in a note or an
 * answer must never be followed as-is.
 */
export function isLocalHref(href) {
  return !!href && !href.startsWith("#") && !href.startsWith("//") && !/^[a-z][a-z0-9+.-]*:/i.test(href);
}

const safeDecode = (text) => {
  try {
    return decodeURIComponent(text);
  } catch {
    return text;
  }
};

/** Turns a relative link to nothing into a missing link: data-create (a note) or data-missing (anything else). */
function missingLink(token, target, from = "") {
  const name = safeDecode(target).trim();
  const last = name.split("/").pop();
  const noteLike = !!last && (/\.md$/i.test(last) || !/\.[a-z0-9]{1,8}$/i.test(last));
  token.attrSet("href", "#");
  token.attrJoin("class", "wikilink missing");
  if (noteLike) {
    // Markdown links are relative to the note they are in.
    const dir = from.includes("/") ? from.slice(0, from.lastIndexOf("/")) : "";
    const path = name.startsWith("/") ? name : dir ? `${dir}/${name}` : name;
    token.attrSet("data-create", normalize(path).replace(/\.md$/i, ""));
    token.attrSet("role", "button");
    token.attrSet("title", "点击创建");
  } else {
    token.attrSet("data-missing", name || target);
    token.attrSet("title", `找不到 ${name || target}`);
  }
}

const VISUAL_LANGS = new Set(["visual", "svg", "chart"]);

function richBlocksPlugin(md) {
  const defaultFence = md.renderer.rules.fence;
  md.renderer.rules.fence = (tokens, idx, options, env, self) => {
    const token = tokens[idx];
    const lang = token.info.trim().split(/\s+/)[0].toLowerCase();
    // URI-encoded so the payload survives sanitising untouched (no < or > inside the attribute).
    const code = encodeURIComponent(token.content);
    if (lang === "mermaid") return `<div class="rich-block mermaid-block" data-code="${code}"></div>`;
    if (VISUAL_LANGS.has(lang)) return `<div class="rich-block visual-block" data-kind="${lang}" data-code="${code}"></div>`;
    return defaultFence(tokens, idx, options, env, self);
  };
}

function headingIdsPlugin(md) {
  md.core.ruler.push("heading_ids", (state) => {
    let count = 0;
    for (const token of state.tokens) {
      if (token.type === "heading_open") token.attrSet("id", `h-${count++}`);
    }
  });
}

const md = new MarkdownIt({
  html: true,
  linkify: true,
  breaks: true,
  highlight(code, lang) {
    if (lang && hljs.getLanguage(lang)) {
      try {
        return `<pre class="hljs"><code>${hljs.highlight(code, { language: lang, ignoreIllegals: true }).value}</code></pre>`;
      } catch {}
    }
    return `<pre class="hljs"><code>${escapeHtml(code)}</code></pre>`;
  },
})
  .use(texmath, { engine: katex, delimiters: ["dollars", "brackets"], katexOptions: { throwOnError: false, strict: false } })
  .use(wikilinkPlugin)
  .use(localLinksPlugin)
  .use(richBlocksPlugin)
  .use(headingIdsPlugin);

md.enable(["table", "strikethrough"]);
// Only link explicit URLs: bare names like "AGENTS.md" are notes, not the .md TLD.
md.linkify.set({ fuzzyLink: false, fuzzyEmail: false });

DOMPurify.addHook("uponSanitizeElement", (node, data) => {
  if (data.tagName === "iframe") {
    const src = node.getAttribute("src") || "";
    if (!src.startsWith(`${API_BASE}/api/file?`)) node.remove();
  }
});

export function splitFrontmatter(text) {
  const match = /^---[ \t]*\n([\s\S]*?)\n---[ \t]*(?:\n|$)/.exec(text);
  if (!match) return { meta: null, body: text, offset: 0 };
  let meta = null;
  try {
    meta = parseYaml(match[1]);
  } catch {}
  if (!meta || typeof meta !== "object") meta = null;
  return { meta, body: text.slice(match[0].length), offset: match[0].split("\n").length - 1 };
}

function renderMeta(meta) {
  const rows = Object.entries(meta)
    .map(([key, value]) => {
      const list = Array.isArray(value) ? value : [value];
      const cells = list
        .map((v) => {
          const text = typeof v === "object" ? JSON.stringify(v) : String(v ?? "");
          return key === "tags" ? `<span class="tag">#${escapeHtml(text)}</span>` : escapeHtml(text);
        })
        .join(key === "tags" ? " " : ", ");
      return `<tr><th>${escapeHtml(key)}</th><td>${cells}</td></tr>`;
    })
    .join("");
  return `<table class="properties">${rows}</table>`;
}

function calloutify(html) {
  return html.replace(
    /<blockquote>\s*<p>\[!(\w+)\]([+-]?)\s*([^\n<]*)(?:<br>\n?)?/g,
    (_, type, __, title) =>
      `<blockquote class="callout callout-${type.toLowerCase()}"><div class="callout-title">${title || type}</div><p>`,
  );
}

// Under `mygo dev` vault files are served from the app's own scheme (API_BASE =
// "mygo://localhost"), which DOMPurify's default URI allow-list does not know.
const SANITIZE_URIS = API_BASE.startsWith("mygo:")
  ? { ALLOWED_URI_REGEXP: /^(?:(?:(?:f|ht)tps?|mailto|tel|callto|sms|cid|xmpp|mygo):|[^a-z]|[a-z+.-]+(?:[^a-z+.\-:]|$))/i }
  : {};

const FORBIDDEN_TAGS = ["form", "input", "button", "select", "textarea", "option", "optgroup", "datalist", "fieldset", "output", "style"];

export function renderMarkdown(text, { path = "", resolver = null, frontmatter = true } = {}) {
  const { meta, body } = frontmatter ? splitFrontmatter(text || "") : { meta: null, body: text || "" };
  let html = md.render(body, { path, resolver });
  html = calloutify(html);
  if (meta && Object.keys(meta).length) html = renderMeta(meta) + html;
  return DOMPurify.sanitize(html, {
    ADD_TAGS: ["iframe"],
    ADD_ATTR: ["target", "loading", "data-note", "data-anchor", "data-create", "data-missing", "data-code", "data-kind"],
    // Notes and AI answers come from anywhere (a synced vault, an agent, a prompt
    // injection) and render inside the app's own origin: no forms or controls
    // that could post to the app's API, and no stylesheets. Copy buttons and
    // task boxes are added after sanitising (Rich.jsx).
    FORBID_TAGS: FORBIDDEN_TAGS,
    ...SANITIZE_URIS,
  });
}

export function outline(text) {
  const { body, offset } = splitFrontmatter(text || "");
  const tokens = md.parse(body, {});
  const items = [];
  for (let i = 0; i < tokens.length; i++) {
    if (tokens[i].type === "heading_open") {
      items.push({
        level: Number(tokens[i].tag.slice(1)),
        text: tokens[i + 1].content.replace(/\[\[(?:[^\]|]*\|)?([^\]]*)\]\]/g, "$1"),
        id: `h-${items.length}`,
        line: (tokens[i].map?.[0] ?? 0) + offset + 1,
      });
    }
  }
  return items;
}
