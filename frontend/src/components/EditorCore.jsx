import { forwardRef, useContext, useEffect, useImperativeHandle, useRef } from "react";
import { Decoration, EditorView, MatchDecorator, ViewPlugin, keymap, placeholder } from "@codemirror/view";
import { EditorState, RangeSetBuilder } from "@codemirror/state";
import { HighlightStyle, syntaxHighlighting, syntaxTree } from "@codemirror/language";
import { basicSetup } from "codemirror";
import { indentWithTab } from "@codemirror/commands";
import { markdown, markdownLanguage } from "@codemirror/lang-markdown";
import { languages } from "@codemirror/language-data";
// Ships with @codemirror/language-data (hoisted in package-lock): parses a leading
// --- YAML --- block as frontmatter instead of a Setext heading.
import { yamlFrontmatter } from "@codemirror/lang-yaml";
// @lezer/highlight ships with @codemirror/language (hoisted in package-lock); its tags name what the parsers found.
import { tags as t } from "@lezer/highlight";
import AppContext from "../app/AppContext.jsx";
import { api } from "../lib/api.js";
import { isMac } from "../lib/native.js";
import { toast } from "../ui/Toast.jsx";

// ------------------------------------------------------------------ look
/**
 * The editor's chrome, all from the design tokens so it follows light/dark:
 * prose in the UI font at reading size, the column aligned with the preview's,
 * an accent caret, quiet search panel and completion popover in Magpie's style.
 */
const theme = EditorView.theme({
  "&": { height: "100%", fontSize: "15px", backgroundColor: "transparent", color: "var(--fg)" },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--font)", lineHeight: "1.75", padding: "30px 0 40vh", overscrollBehavior: "contain" },
  // the column matches the preview's; its gutters narrow with the pane (split view, small windows)
  ".cm-content": { maxWidth: "780px", margin: "0 auto", padding: "0 clamp(16px, 5%, 36px)", caretColor: "var(--accent)" },
  ".cm-line": { padding: "0 2px" },
  ".cm-gutters": { display: "none" },
  ".cm-activeLine": { backgroundColor: "transparent" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--accent)", borderLeftWidth: "2px" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": {
    backgroundColor: "var(--selection) !important",
  },
  ".cm-selectionMatch": { backgroundColor: "color-mix(in srgb, var(--accent) 12%, transparent)", borderRadius: "2px" },
  ".cm-matchingBracket, &.cm-focused .cm-matchingBracket": { backgroundColor: "color-mix(in srgb, var(--accent) 16%, transparent)", outline: "none" },
  ".cm-nonmatchingBracket, &.cm-focused .cm-nonmatchingBracket": { backgroundColor: "var(--red-soft)" },
  ".cm-placeholder": { color: "var(--faint)" },
  ".cm-foldPlaceholder": { backgroundColor: "var(--pill)", border: "none", color: "var(--muted)", borderRadius: "4px", padding: "0 5px" },

  // fenced code: a soft block in the code font
  ".cm-codeblock": { fontFamily: "var(--code)", fontSize: "13px", lineHeight: "1.65", backgroundColor: "var(--card-2)", padding: "0 14px !important", boxShadow: "inset 1px 0 var(--line-2), inset -1px 0 var(--line-2)" },
  ".cm-codeblock-open": { borderTopLeftRadius: "9px", borderTopRightRadius: "9px", paddingTop: "4px !important", boxShadow: "inset 1px 0 var(--line-2), inset -1px 0 var(--line-2), inset 0 1px var(--line-2)" },
  ".cm-codeblock-close": { borderBottomLeftRadius: "9px", borderBottomRightRadius: "9px", paddingBottom: "4px !important", boxShadow: "inset 1px 0 var(--line-2), inset -1px 0 var(--line-2), inset 0 -1px var(--line-2)" },
  ".cm-codeblock-open.cm-codeblock-close": { boxShadow: "inset 0 0 0 1px var(--line-2)" },
  // [[wikilinks]]: the target in the link colour, its brackets faint
  ".cm-wikilink, .cm-wikilink *": { color: "var(--accent)", textDecoration: "none" },
  ".cm-wikilink": { textDecoration: "underline", textDecorationColor: "color-mix(in srgb, var(--accent) 35%, transparent)", textUnderlineOffset: "3px" },
  ".cm-wikilink-mark, .cm-wikilink-mark *": { color: "var(--faint)" },
  // $maths$ and $$ … $$ blocks: their TeX source in the code font, not parsed as Markdown
  ".cm-math, .cm-math-line": { fontFamily: "var(--code)", fontSize: ".88em", color: "var(--tk-v)" },
  ".cm-math *, .cm-math-line *": { color: "inherit", fontFamily: "inherit", fontSize: "inherit", fontStyle: "normal", fontWeight: "inherit", textDecoration: "none" },

  // search / replace panel
  ".cm-panels": { backgroundColor: "var(--card-2)", color: "var(--fg)", fontFamily: "var(--font)" },
  ".cm-panels.cm-panels-top": { borderBottom: "1px solid var(--line-2)" },
  ".cm-panels.cm-panels-bottom": { borderTop: "1px solid var(--line-2)" },
  ".cm-panel.cm-search": { display: "flex", flexWrap: "wrap", alignItems: "center", gap: "6px", padding: "8px 36px 8px 12px", fontSize: "12.5px" },
  ".cm-panel.cm-search br": { display: "none" },
  ".cm-panel.cm-search label": { display: "inline-flex", alignItems: "center", gap: "5px", fontSize: "12px", color: "var(--fg-2)" },
  ".cm-panel.cm-search [name=close]": { position: "absolute", top: "8px", right: "10px", width: "22px", height: "22px", borderRadius: "6px", color: "var(--muted)", fontSize: "16px", lineHeight: "20px", padding: "0" },
  ".cm-panel.cm-search [name=close]:hover": { backgroundColor: "var(--pill)", color: "var(--fg)" },
  ".cm-textfield": {
    height: "25px", margin: "0", padding: "0 8px", border: "1px solid var(--line)", borderRadius: "6px",
    backgroundColor: "var(--card)", color: "var(--fg)", fontSize: "12.5px", outline: "none",
  },
  ".cm-textfield:focus": { borderColor: "var(--accent)", boxShadow: "0 0 0 3px color-mix(in srgb, var(--accent) 20%, transparent)" },
  ".cm-button": {
    height: "25px", margin: "0", padding: "0 10px", border: "1px solid var(--line)", borderRadius: "6px",
    backgroundImage: "none", backgroundColor: "var(--card)", color: "var(--fg-2)", fontSize: "12px",
  },
  ".cm-button:hover": { backgroundColor: "var(--pill)", color: "var(--fg)" },
  ".cm-button:active": { backgroundImage: "none", backgroundColor: "var(--pill-hover)" },
  ".cm-searchMatch": { backgroundColor: "var(--search-hit)", borderRadius: "2px" },
  ".cm-searchMatch.cm-searchMatch-selected": { backgroundColor: "color-mix(in srgb, var(--c4) 70%, transparent)" },

  // completion and other tooltips: a frosted popover
  ".cm-tooltip": {
    backgroundColor: "var(--pop-bg)", color: "var(--fg)", border: "none", borderRadius: "10px", boxShadow: "var(--shadow)",
    backdropFilter: "blur(24px) saturate(1.4)", WebkitBackdropFilter: "blur(24px) saturate(1.4)", overflow: "hidden",
  },
  ".cm-tooltip.cm-tooltip-autocomplete > ul": { fontFamily: "var(--font)", padding: "4px", maxHeight: "16em", minWidth: "220px" },
  ".cm-tooltip.cm-tooltip-autocomplete > ul > li": { display: "flex", alignItems: "baseline", gap: "8px", padding: "4px 8px", borderRadius: "6px", lineHeight: "1.45", fontSize: "12.5px" },
  ".cm-tooltip.cm-tooltip-autocomplete > ul > li[aria-selected]": { backgroundColor: "var(--sel)", color: "var(--fg)" },
  ".cm-completionIcon": { display: "none" },
  ".cm-completionLabel": { flex: "none" },
  ".cm-completionMatchedText": { textDecoration: "none", fontWeight: "600", color: "var(--accent)" },
  ".cm-completionDetail": { marginLeft: "auto", fontStyle: "normal", fontSize: "11px", color: "var(--muted)", overflow: "hidden", textOverflow: "ellipsis" },
  ".cm-tooltip-section:not(:first-child)": { borderTop: "1px solid var(--line-2)" },
});

/** Markdown typography and code colours (the --tk-* tokens Magpie uses for code). */
const highlight = HighlightStyle.define([
  { tag: t.heading1, fontSize: "1.55em", fontWeight: "650", letterSpacing: "-.02em", lineHeight: "1.4" },
  { tag: t.heading2, fontSize: "1.3em", fontWeight: "650", letterSpacing: "-.01em" },
  { tag: t.heading3, fontSize: "1.13em", fontWeight: "600" },
  { tag: [t.heading4, t.heading5, t.heading6], fontWeight: "600" },
  { tag: t.strong, fontWeight: "650" },
  { tag: t.emphasis, fontStyle: "italic" },
  { tag: t.strikethrough, textDecoration: "line-through", color: "var(--muted)" },
  { tag: t.link, color: "var(--accent)" },
  { tag: t.url, color: "var(--muted)", textDecoration: "underline", textDecorationColor: "var(--line)" },
  { tag: t.monospace, fontFamily: "var(--code)", fontSize: ".88em" },
  { tag: t.quote, color: "var(--fg-2)" },
  { tag: [t.processingInstruction, t.contentSeparator], color: "var(--faint)" },
  { tag: t.labelName, color: "var(--muted)" },
  { tag: [t.escape, t.character], color: "var(--tk-o)" },
  { tag: [t.meta, t.documentMeta], color: "var(--muted)" },
  // code inside fences
  { tag: [t.keyword, t.modifier, t.operatorKeyword, t.controlKeyword, t.definitionKeyword, t.moduleKeyword], color: "var(--tk-k)" },
  { tag: [t.string, t.special(t.string), t.regexp], color: "var(--tk-s)" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "var(--tk-n)" },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.className, t.typeName, t.namespace], color: "var(--tk-f)" },
  { tag: [t.propertyName, t.attributeName, t.special(t.variableName)], color: "var(--tk-v)" },
  { tag: [t.tagName, t.angleBracket, t.self], color: "var(--tk-o)" },
  { tag: [t.comment, t.lineComment, t.blockComment, t.docComment], color: "var(--tk-c)", fontStyle: "italic" },
  { tag: t.invalid, color: "var(--red)" },
]);

// ------------------------------------------------------------ decorations
const CODE_LINE = {
  only: Decoration.line({ class: "cm-codeblock cm-codeblock-open cm-codeblock-close" }),
  open: Decoration.line({ class: "cm-codeblock cm-codeblock-open" }),
  mid: Decoration.line({ class: "cm-codeblock" }),
  close: Decoration.line({ class: "cm-codeblock cm-codeblock-close" }),
};

const BLOCK_NODES = new Set(["FencedCode", "CodeBlock", "Frontmatter"]);

/** Line decorations that set fenced / indented code and the YAML frontmatter apart as soft blocks. */
function codeBlockDecorations(view) {
  const builder = new RangeSetBuilder();
  const { state } = view;
  let lastLine = 0;
  for (const { from, to } of view.visibleRanges) {
    syntaxTree(state).iterate({
      from,
      to,
      enter(node) {
        if (!BLOCK_NODES.has(node.name)) return undefined;
        const first = state.doc.lineAt(node.from).number;
        // A node that takes its trailing newline ends at the start of the next line.
        const end = state.doc.lineAt(node.to);
        const last = end.from === node.to && node.to > node.from ? end.number - 1 : end.number;
        for (let n = Math.max(first, lastLine + 1); n <= last; n++) {
          const deco = first === last ? CODE_LINE.only : n === first ? CODE_LINE.open : n === last ? CODE_LINE.close : CODE_LINE.mid;
          builder.add(state.doc.line(n).from, state.doc.line(n).from, deco);
          lastLine = n;
        }
        return false;
      },
    });
  }
  return builder.finish();
}

const codeBlocks = ViewPlugin.fromClass(
  class {
    constructor(view) {
      this.decorations = codeBlockDecorations(view);
    }
    update(update) {
      if (update.docChanged || update.viewportChanged || syntaxTree(update.startState) !== syntaxTree(update.state)) {
        this.decorations = codeBlockDecorations(update.view);
      }
    }
  },
  { decorations: (plugin) => plugin.decorations },
);

const WIKI_BRACKETS = Decoration.mark({ class: "cm-wikilink-mark" });
const WIKI_TARGET = Decoration.mark({ class: "cm-wikilink" });
const wikilinkMatcher = new MatchDecorator({
  regexp: /(!?\[\[)[^\]\n]+\]\]/g,
  decorate(add, from, to, match) {
    const open = from + match[1].length;
    add(from, open, WIKI_BRACKETS);
    add(open, to - 2, WIKI_TARGET);
    add(to - 2, to, WIKI_BRACKETS);
  },
});

/** Colours [[wikilinks]] and ![[embeds]] like links (the Markdown parser doesn't know them). */
const wikilinks = ViewPlugin.fromClass(
  class {
    constructor(view) {
      this.decorations = wikilinkMatcher.createDeco(view);
    }
    update(update) {
      this.decorations = wikilinkMatcher.updateDeco(update, this.decorations);
    }
  },
  { decorations: (plugin) => plugin.decorations },
);

// ------------------------------------------------------------------ maths
const MATH = Decoration.mark({ class: "cm-math" });
const MATH_LINE = Decoration.line({ class: "cm-math-line" });
const CODE_NODES = new Set(["InlineCode", "FencedCode", "CodeBlock", "Frontmatter"]);

/** True when `pos` sits in code (inline or fenced) or the frontmatter, where a $ is just a dollar. */
function inCode(state, pos) {
  for (let node = syntaxTree(state).resolveInner(pos, 1); node; node = node.parent) if (CODE_NODES.has(node.name)) return true;
  return false;
}

// $$…$$ on one line, or $…$ that hugs its content ("$5 and $6" is not maths).
// The leading group stands in for a lookbehind (older WebKit has none).
const mathMatcher = new MatchDecorator({
  regexp: /(^|[^\\$\w])(\$\$[^$\n]+?\$\$|\$[^\s$](?:[^$\n]*?[^\s\\$])?\$)(?![\w$])/g,
  decorate(add, from, to, match, view) {
    const start = from + match[1].length;
    if (!inCode(view.state, start)) add(start, to, MATH);
  },
});

/** Marks inline maths (the Markdown parser would read its _ and * as emphasis). */
const inlineMath = ViewPlugin.fromClass(
  class {
    constructor(view) {
      this.decorations = mathMatcher.createDeco(view);
    }
    update(update) {
      this.decorations = mathMatcher.updateDeco(update, this.decorations);
    }
  },
  { decorations: (plugin) => plugin.decorations },
);

const FENCE = /^(`{3,}|~{3,})/;

/** Line numbers of multi-line $$ … $$ blocks outside code fences (an unclosed $$ marks nothing). */
function mathBlockLines(doc) {
  const lines = new Set();
  let fence = null;
  let open = 0;
  for (let n = 1; n <= doc.lines; n++) {
    const text = doc.line(n).text.trim();
    if (open) {
      if (text.endsWith("$$")) {
        for (let k = open; k <= n; k++) lines.add(k);
        open = 0;
      }
      continue;
    }
    const marker = FENCE.exec(text)?.[1];
    if (fence) {
      if (marker && marker[0] === fence[0] && marker.length >= fence.length && !text.slice(marker.length).trim()) fence = null;
    } else if (marker) {
      fence = marker;
    } else if (text.startsWith("$$") && !(text.length >= 4 && text.endsWith("$$"))) {
      open = n;
    }
  }
  return lines;
}

/** Sets display-maths blocks spanning several lines in the maths style. */
const mathBlocks = ViewPlugin.fromClass(
  class {
    constructor(view) {
      this.lines = mathBlockLines(view.state.doc);
      this.decorations = this.build(view);
    }
    update(update) {
      if (update.docChanged) this.lines = mathBlockLines(update.state.doc);
      if (update.docChanged || update.viewportChanged) this.decorations = this.build(update.view);
    }
    build(view) {
      const builder = new RangeSetBuilder();
      if (!this.lines.size) return builder.finish();
      const { doc } = view.state;
      let last = 0;
      for (const { from, to } of view.visibleRanges) {
        for (let line = doc.lineAt(from); ; line = doc.line(line.number + 1)) {
          if (line.number > last && this.lines.has(line.number)) {
            builder.add(line.from, line.from, MATH_LINE);
            last = line.number;
          }
          if (line.to >= to || line.number >= doc.lines) break;
        }
      }
      return builder.finish();
    }
  },
  { decorations: (plugin) => plugin.decorations },
);

// ------------------------------------------------------------- completion
const noteName = (path) => path.split("/").pop().replace(/\.md$/i, "");

/**
 * Completes `[[` with the vault's notes (and files, for ![[embeds]]): the
 * name alone when it is unique, the path otherwise — as Obsidian links.
 */
function wikilinkCompletion(getFiles) {
  return (context) => {
    const match = context.matchBefore(/\[\[[^\][\n|#^]*$/);
    if (!match) return null;
    const files = getFiles();
    if (!files.length) return null;
    const counts = new Map();
    for (const path of files) counts.set(noteName(path), (counts.get(noteName(path)) || 0) + 1);
    const options = files.map((path) => {
      const name = noteName(path);
      const isNote = /\.md$/i.test(path);
      const target = counts.get(name) > 1 ? (isNote ? path.replace(/\.md$/i, "") : path) : isNote ? name : path.split("/").pop();
      const folder = path.split("/").slice(0, -1).join("/");
      return {
        label: target,
        detail: folder || (isNote ? "" : "附件"),
        boost: isNote ? 1 : -1,
        apply: (view, _completion, from, to) => {
          const closed = view.state.sliceDoc(to, to + 2) === "]]";
          const insert = closed ? target : `${target}]]`;
          view.dispatch({
            changes: { from, to, insert },
            selection: { anchor: from + target.length + 2 },
            userEvent: "input.complete",
          });
        },
      };
    });
    return { from: match.from + 2, options, validFor: /^[^\][\n|#^]*$/ };
  };
}

// ----------------------------------------------------------------- helpers
/** Length of the common prefix and suffix of two strings (the suffix never overlaps the prefix). */
function commonEnds(a, b) {
  const max = Math.min(a.length, b.length);
  let start = 0;
  while (start < max && a.charCodeAt(start) === b.charCodeAt(start)) start++;
  let end = 0;
  while (end < max - start && a.charCodeAt(a.length - 1 - end) === b.charCodeAt(b.length - 1 - end)) end++;
  return { start, end };
}

/** CodeMirror's own line ends: every \r\n or lone \r is a \n in the document. */
const toLF = (text) => String(text ?? "").replace(/\r\n?/g, "\n");

const PLACEHOLDER = `开始写作…  ${isMac ? "⌘S" : "Ctrl+S"} 保存 · 粘贴或拖入图片 · 输入 [[ 链接笔记`;

/**
 * The Markdown source editor (CodeMirror 6). Loaded on demand by Editor.jsx,
 * which keeps CodeMirror out of the app's first chunk.
 *
 * Imperative handle: `getSelection()`, `replaceSelection(text)`,
 * `insertAtCursor(text)`, `replaceAll(text)`, `gotoLine(n)`.
 *
 * A new `docKey` means `value` is another document (another note): the editor
 * starts a fresh state, so undo can never bring back the previous note's text,
 * however alike the two notes are. A new `value` under the same key (a reload
 * from disk, an applied AI result) is applied as a minimal change, so
 * selection, scroll and undo survive it; only a value that shares almost
 * nothing with the document replaces it outright.
 *
 * The document always has \n line ends (CodeMirror's own); a value with \r\n
 * is compared and applied as \n, so it never looks like an edit.
 *
 * @param {object} props
 * @param {string} props.value
 * @param {string|number} [props.docKey]     identifies the document `value` belongs to
 * @param {(text: string) => void} [props.onChange]
 * @param {() => void} [props.onSave]          ⌘S / Ctrl+S
 * @param {(text: string) => void} [props.onSelection]
 * @param {string} [props.assetsDir]           where pasted / dropped files are uploaded
 */
const EditorCore = forwardRef(function EditorCore({ value, docKey, onChange, onSave, onSelection, assetsDir }, ref) {
  const app = useContext(AppContext);
  const host = useRef(null);
  const view = useRef(null);
  const extensions = useRef(null);
  // The document the editor holds (see docKey); bumped by every fresh state.
  const lastKey = useRef(docKey);
  const generation = useRef(0);
  const callbacks = useRef({});
  callbacks.current = { onChange, onSave, onSelection, assetsDir, files: app?.files ?? [], notify: app?.notifyVaultChanged };

  useImperativeHandle(ref, () => ({
    getSelection() {
      const v = view.current;
      if (!v) return "";
      const { from, to } = v.state.selection.main;
      return v.state.sliceDoc(from, to);
    },
    replaceSelection(text) {
      const v = view.current;
      if (!v) return;
      v.dispatch(v.state.replaceSelection(text));
      v.focus();
    },
    insertAtCursor(text) {
      const v = view.current;
      if (!v) return;
      const { to } = v.state.selection.main;
      v.dispatch({ changes: { from: to, insert: text }, selection: { anchor: to + text.length }, scrollIntoView: true });
      v.focus();
    },
    replaceAll(text) {
      const v = view.current;
      if (!v) return;
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: text } });
    },
    gotoLine(line) {
      const v = view.current;
      if (!v) return;
      const target = v.state.doc.line(Math.min(Math.max(line, 1), v.state.doc.lines));
      v.dispatch({ selection: { anchor: target.from }, effects: EditorView.scrollIntoView(target.from, { y: "start", yMargin: 40 }) });
      v.focus();
    },
  }));

  useEffect(() => {
    let destroyed = false;
    /**
     * Uploads pasted / dropped files and links them at the caret — only into the
     * note they were dropped on: if another note was opened (or the editor
     * closed) meanwhile, the files are kept but not linked. The app is told
     * about the new files, so the tree and the preview's links know them.
     */
    const uploadAndInsert = async (files, v) => {
      const generationAtStart = generation.current;
      const stored = [];
      let unlinked = 0;
      for (const file of files) {
        try {
          const { path } = await api.upload(file, callbacks.current.assetsDir);
          stored.push(path);
          if (destroyed || generation.current !== generationAtStart) {
            unlinked += 1;
            continue;
          }
          v.dispatch(v.state.replaceSelection(`![[${path}]]\n`));
        } catch (err) {
          toast.err(`上传 ${file.name} 失败：${err.message}`);
        }
      }
      if (stored.length) callbacks.current.notify?.(stored);
      if (unlinked) toast(unlinked === 1 ? "已上传，但笔记已切换，未插入链接" : `已上传 ${unlinked} 个文件，但笔记已切换，未插入链接`);
    };
    extensions.current = [
      basicSetup,
      yamlFrontmatter({ content: markdown({ base: markdownLanguage, codeLanguages: languages }) }),
      markdownLanguage.data.of({ autocomplete: wikilinkCompletion(() => callbacks.current.files) }),
      EditorView.lineWrapping,
      theme,
      syntaxHighlighting(highlight),
      codeBlocks,
      wikilinks,
      inlineMath,
      mathBlocks,
      placeholder(PLACEHOLDER),
      keymap.of([{ key: "Mod-s", preventDefault: true, run: () => (callbacks.current.onSave?.(), true) }, indentWithTab]),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) callbacks.current.onChange?.(update.state.doc.toString());
        if (update.selectionSet) {
          const { from, to } = update.state.selection.main;
          callbacks.current.onSelection?.(update.state.sliceDoc(from, to));
        }
      }),
      EditorView.domEventHandlers({
        paste(event, v) {
          const files = [...(event.clipboardData?.files || [])];
          if (!files.length) return false;
          event.preventDefault();
          uploadAndInsert(files, v);
          return true;
        },
        drop(event, v) {
          const files = [...(event.dataTransfer?.files || [])];
          if (!files.length) return false;
          event.preventDefault();
          const pos = v.posAtCoords({ x: event.clientX, y: event.clientY });
          if (pos != null) v.dispatch({ selection: { anchor: pos } });
          uploadAndInsert(files, v);
          return true;
        },
      }),
    ];
    view.current = new EditorView({ state: EditorState.create({ doc: toLF(value), extensions: extensions.current }), parent: host.current });
    return () => {
      destroyed = true;
      view.current.destroy();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // A value from outside: another note, a reload from disk, an applied AI result.
  useEffect(() => {
    const v = view.current;
    if (!v) return;
    const next = toLF(value);
    const fresh = () => {
      generation.current += 1;
      v.setState(EditorState.create({ doc: next, extensions: extensions.current }));
    };
    // Another document — even one with the same text: no history carries over.
    if (docKey !== lastKey.current) {
      lastKey.current = docKey;
      fresh();
      v.scrollDOM.scrollTop = 0;
      return;
    }
    const current = v.state.doc.toString();
    if (next === current) return;
    const { start, end } = commonEnds(current, next);
    const longest = Math.max(current.length, next.length);
    if (start + end < longest * 0.3) {
      fresh();
      return;
    }
    v.dispatch({ changes: { from: start, to: current.length - end, insert: next.slice(start, next.length - end) } });
  }, [value, docKey]);

  return <div className="editor" ref={host} />;
});

export default EditorCore;
