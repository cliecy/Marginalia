import { Fragment, memo, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import AppContext from "../app/AppContext.jsx";
import { useModels } from "../app/hooks.js";
import { timeAgo } from "../app/format.js";
import { api, ApiError, stream } from "../lib/api.js";
import { renderMarkdown } from "../lib/markdown.js";
import Button, { IconButton } from "../ui/Button.jsx";
import { CopyButton, copyText } from "../ui/CopyField.jsx";
import { SearchInput, Select } from "../ui/Field.jsx";
import Icon from "../ui/Icon.jsx";
import { Pill } from "../ui/Pill.jsx";
import { MenuButton, Popover } from "../ui/Popover.jsx";
import Segmented from "../ui/Segmented.jsx";
import Spinner from "../ui/Spinner.jsx";
import { toast } from "../ui/Toast.jsx";
import { isComposingKey } from "../ui/keys.js";
import { routeLinkClick } from "./Preview.jsx";
import Rich from "./Rich.jsx";

// ------------------------------------------------------------------ actions
const ONLY = "只输出结果本身，不要任何解释或前后缀。保留 Markdown 格式、公式和 [[双链]]。";

/**
 * Quick actions on the open note or the selected text. `apply` says what the
 * answer offers to do with the note: "replace" the selection, "replaceAll",
 * "append", or "slides" (save as a new note and present). `whole` actions
 * always work on the whole note. `group` arranges the chips.
 */
export const ACTIONS = [
  { id: "polish", label: "润色", group: "rewrite", prompt: `润色下面的文本，使其更通顺、准确、专业，保持原意和原语言。${ONLY}`, apply: "replace" },
  { id: "fix", label: "纠错", group: "rewrite", prompt: `修正下面文本中的错别字、语法和标点错误，不改变内容和风格。${ONLY}`, apply: "replace" },
  { id: "shorter", label: "精简", group: "rewrite", prompt: `在不丢失关键信息的前提下精简下面的文本。${ONLY}`, apply: "replace" },
  { id: "longer", label: "扩写", group: "rewrite", prompt: `扩写下面的文本，补充必要的解释、例子与细节，保持风格一致。${ONLY}`, apply: "replace" },
  { id: "explain", label: "解释", group: "understand", prompt: "用通俗易懂的方式解释下面的内容，必要时举例或类比。" },
  { id: "summary", label: "总结", group: "understand", prompt: "用要点列表总结下面内容的核心观点。" },
  { id: "zh", label: "译中", group: "translate", prompt: `把下面的文本翻译成中文。${ONLY}`, apply: "replace" },
  { id: "en", label: "译英", group: "translate", prompt: `Translate the following text into natural, academic English. ${ONLY}`, apply: "replace" },
  { id: "ja", label: "译日", group: "translate", prompt: `次の文章を自然な日本語に翻訳してください。${ONLY}`, apply: "replace" },
  { id: "continue", label: "续写", group: "create", prompt: `根据这篇笔记的内容与风格，续写接下来最自然的部分。只输出续写的新内容。`, whole: true, apply: "append" },
  {
    id: "structure",
    label: "整理本篇",
    group: "create",
    prompt: `按照知识库约定重新整理这篇笔记：补全 frontmatter（summary、tags 等），合理分节、合并重复内容、为提到的概念加 [[双链]]，不丢失任何信息。只输出完整的新笔记内容。`,
    whole: true,
    apply: "replaceAll",
  },
  {
    id: "diagram",
    label: "画图解",
    group: "create",
    prompt: "为下面的内容画一张最能帮助理解的图：概念关系/流程/层次/时间线优先用 ```mermaid；需要自由排版、对比卡片、示意图或带交互的讲解时用 ```visual（HTML+SVG，使用约定里的 CSS 变量配色）；数据用 ```chart。只输出这一个代码块，可在前面加一句话说明。",
    apply: "append",
  },
  {
    id: "slides",
    label: "做成幻灯片",
    group: "create",
    prompt: "把下面的内容改编成一套讲解用的幻灯片（Markdown，用单独一行的 --- 分页）：第一页是标题页；每页一个要点、不超过 6 行；适当的页面配 ```mermaid 图或 ```visual 示意图；最后一页总结。只输出幻灯片 Markdown 本身（不要 frontmatter）。",
    whole: true,
    apply: "slides",
  },
  { id: "quiz", label: "出题自测", group: "understand", prompt: "根据下面的内容出 5 道由浅入深的自测题，每题答案放在 Obsidian 折叠 callout（> [!note]- 答案）中。", whole: true },
];

const GROUPS = ["rewrite", "understand", "translate", "create"];
const actionTitle = (action) => (action.whole ? `${action.label}整篇笔记` : `${action.label}选中的文本；没有选中时作用于整篇笔记`);

const SCOPES = [
  { value: "none", label: "不带上下文", title: "只发送对话本身" },
  { value: "note", label: "当前笔记", title: "附上正在看的笔记" },
  { value: "vault", label: "整个知识库", title: "让 AI 参考整个知识库（较慢）" },
];
const THINKING = { vault: "正在翻阅知识库…", note: "正在阅读这篇笔记…", none: "思考中…" };

const EMPTY_CHAT = Object.freeze({ path: null, etag: null, title: "", status: null });

// ------------------------------------------------------------------ helpers
const readPref = (key) => {
  try {
    return localStorage.getItem(key) || "";
  } catch {
    return "";
  }
};
const writePref = (key, value) => {
  try {
    localStorage.setItem(key, value);
  } catch {}
};

const oneLine = (text) => String(text || "").replace(/\s+/g, " ").trim();

/** "2026-10-20 10:30" and ISO strings → "3 天前" (WebKit cannot parse the space form). */
const when = (value) => timeAgo(String(value || "").replace(" ", "T")) || String(value || "").slice(0, 16);

/** The chat's title: its first typed question, else the first quick action. */
function titleFrom(messages) {
  const typed = messages.find((m) => m.role === "user" && !m.action);
  const action = messages.find((m) => m.role === "user" && m.action);
  const text = typed?.content || action?.actionLabel || "对话";
  return oneLine(text).slice(0, 40) || "对话";
}

/** Recognises quick actions in a loaded chat, so they show as actions (with their apply buttons) again. */
function decorate(messages) {
  let source = null;
  return (messages || []).map((m) => {
    if (m.role !== "user") return { role: m.role, content: m.content, source };
    const action = ACTIONS.find((a) => m.content?.startsWith(a.prompt));
    if (!action) {
      source = null;
      return { role: "user", content: m.content };
    }
    const text = /\n\n<<<\n([\s\S]*)\n>>>\s*$/.exec(m.content)?.[1] ?? "";
    source = { apply: action.apply, original: action.whole ? null : text || null };
    return {
      role: "user",
      content: m.content,
      action: true,
      actionLabel: action.label,
      actionScope: action.whole ? "整篇笔记" : `${text.length} 字`,
      display: `✦ ${action.label}`,
      source,
      request: { scope: action.whole ? "none" : "note", selection: "" },
    };
  });
}

// The streaming caret: a private-use character appended to the partial answer
// marks where the text ends, and becomes a blinking bar after rendering.
const CARET = "";
const CARET_HTML = '<span class="ai-caret" aria-hidden="true"></span>';

function renderAnswer(text, pending, resolver, path) {
  let html;
  try {
    html = renderMarkdown(pending ? `${text}${CARET}` : text, { frontmatter: false, resolver, path: path || "" });
  } catch {
    html = `<p>${String(text).replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" })[c]).replace(/\n/g, "<br>")}</p>`;
  }
  if (!pending) return html;
  return html.includes(CARET) ? html.replace(CARET, CARET_HTML) : `${html}${CARET_HTML}`;
}

// ------------------------------------------------------------------ pieces
/** Three dots breathing in turn, and what the AI is doing. */
function Thinking({ label }) {
  return (
    <div className="ai-thinking" role="status">
      <span className="ai-dots" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
      {label}
    </div>
  );
}

/** One question: a bubble, or a quick action as an accent tag. */
function UserMessage({ message }) {
  if (message.action) {
    return (
      <div className="ai-msg ai-user">
        <span className="ai-tag" title={message.display}>
          <Icon name="sparkles" size={12} />
          {message.actionLabel || message.display?.replace(/^✦\s*/, "")}
          {message.actionScope && <small>{message.actionScope}</small>}
        </span>
      </div>
    );
  }
  return (
    <div className="ai-msg ai-user">
      <div className="ai-bubble">{message.content}</div>
      {message.attached > 0 && (
        <span className="ai-note">
          <Icon name="at-sign" size={11} />
          附带选中文本 {message.attached} 字
        </span>
      )}
    </div>
  );
}

/** One answer: rendered Markdown, then what can be done with it. */
const AssistantMessage = memo(function AssistantMessage({ message, last, busy, resolver, notePath, onApply, onSaveNote, onRegenerate, onLink }) {
  const html = useMemo(
    () => (message.content ? renderAnswer(message.content, message.pending, resolver, notePath) : ""),
    [message.content, message.pending, resolver, notePath],
  );
  if (message.pending && !message.content) {
    return (
      <div className="ai-msg ai-bot">
        <Thinking label={message.thinking || THINKING.none} />
      </div>
    );
  }
  const source = message.source;
  // The answer comes back trimmed; replacing the trimmed original keeps the
  // selection's surrounding whitespace (a triple-click takes the line break too).
  const original = source?.original?.trim();
  const primary =
    source?.apply === "replace" && original
      ? { label: "替换原文", icon: "edit", run: () => onApply({ type: "replaceText", original, text: message.content }) }
      : source?.apply === "replaceAll"
        ? { label: "预览并替换全文", icon: "columns", run: () => onApply({ type: "replaceAll", text: message.content }) }
        : source?.apply === "slides"
          ? { label: "存为幻灯片并演示", icon: "present", run: () => onApply({ type: "newNote", text: message.content, title: "幻灯片", present: true }) }
          : source?.apply === "append"
            ? { label: "追加到笔记末尾", icon: "arrow-down", run: () => onApply({ type: "append", text: message.content }) }
            : null;
  return (
    <div className={`ai-msg ai-bot ${message.pending ? "pending" : ""}`.trim()}>
      <Rich className="markdown ai-md" html={html} live={!message.pending} onClick={onLink} />
      {!message.pending && (
        <div className="ai-acts">
          {onApply && primary && (
            <button type="button" className="ai-apply" onClick={primary.run}>
              <Icon name={primary.icon} size={12} />
              {primary.label}
            </button>
          )}
          {onApply && <IconButton icon="plus" size="sm" label="插入到光标处" onClick={() => onApply({ type: "insert", text: message.content })} />}
          <CopyButton text={message.content} label="复制回答" />
          {onApply && <IconButton icon="file-plus" size="sm" label="存为新笔记" onClick={onSaveNote} />}
          {last && !busy && <IconButton icon="refresh" size="sm" label="重新生成" onClick={onRegenerate} />}
          {(message.stopped || message.failed) && <span className="ai-meta">{message.failed ? "回答不完整" : "已停止"}</span>}
        </div>
      )}
    </div>
  );
});

/** The saved chats, searchable, in a popover under the history button. */
function ChatHistory({ open, anchor, current, onClose, onPick, onAll }) {
  const [list, setList] = useState(null);
  const [error, setError] = useState(null);
  const [query, setQuery] = useState("");

  useEffect(() => {
    if (!open) return undefined;
    let live = true;
    setQuery("");
    setError(null);
    api
      .chats()
      .then((r) => live && setList(r.chats || []))
      .catch((err) => live && setError(err.message));
    return () => {
      live = false;
    };
  }, [open]);

  const q = query.trim().toLowerCase();
  const shown = (list || []).filter((c) => !q || `${c.title} ${c.path}`.toLowerCase().includes(q));

  let body;
  if (error) body = <div className="ai-hist-none">读取失败：{error}</div>;
  else if (!list)
    body = (
      <div className="ai-hist-none">
        <Spinner size={12} /> 加载中…
      </div>
    );
  else if (!shown.length) body = <div className="ai-hist-none">{list.length ? "没有匹配的对话" : "还没有保存的对话"}</div>;
  else
    body = shown.map((c) => (
      <button key={c.path} type="button" className={`ai-hist-row ${c.path === current ? "cur" : ""}`.trim()} onClick={() => onPick(c.path)} title={c.path}>
        <i className={`ai-hist-dot ${c.status === "inbox" ? "inbox" : "done"}`} aria-hidden="true" />
        <span className="ai-hist-words">
          <span className="ai-hist-title">{c.title}</span>
          <span className="ai-hist-sub">
            {when(c.updated)}
            {c.messages ? ` · ${c.messages} 条消息` : ""}
            {c.status === "inbox" ? " · 待整理" : " · 已整理"}
          </span>
        </span>
        {c.path === current && <Icon name="check" size={13} className="ai-hist-check" />}
      </button>
    ));

  return (
    <Popover open={open} anchor={anchor} onClose={onClose} placement="bottom-end" width={320} className="ai-hist" ariaLabel="历史对话">
      <div className="ai-hist-search">
        <SearchInput size="sm" value={query} onChange={setQuery} placeholder="搜索对话" autoFocus />
      </div>
      <div className="ai-hist-list">{body}</div>
      {onAll && (
        <button type="button" className="ai-hist-foot" onClick={onAll}>
          在「对话」中查看全部
          <Icon name="arrow-right" size={12} />
        </button>
      )}
    </Popover>
  );
}

// ------------------------------------------------------------------ panel
/**
 * The AI assistant beside a note, in Magpie's language: a conversation of
 * quiet bubbles and rendered answers (streamed, with a caret), quick-action
 * chips for the note or the selection, the user's prompt templates, a
 * composer with the context segmented control and a model picker, and the
 * saved-chat history.
 *
 * Conversations with a typed question are saved to Chats/ after each answer
 * (pure quick actions stay ephemeral unless saved by hand); 沉淀 hands the chat
 * to the organize agent.
 *
 * @param {object} props
 * @param {object} props.config            /api/auth/me ({ai_configured, default_model, …})
 * @param {boolean} [props.embedded]       sits under the inspector's tabs (no panel title)
 * @param {string|null} [props.path]       the open note (context and quick actions)
 * @param {string} [props.noteContent]
 * @param {() => string} [props.getSelection]
 * @param {(request: {type, text, original?, title?, present?}) => void} [props.onApply]
 * @param {(path: string) => void} [props.onOpenNote]
 * @param {(chatPath: string) => void} [props.onDistill]
 * @param {() => void} [props.onChatsChanged]
 * @param {string|null} [props.initialChat] load this saved chat when it changes
 * @param {() => void} [props.onInitialChatConsumed]
 * @param {() => void} [props.onClose]     shows a close button when given
 */
export default function AIPanel({
  config,
  embedded = false,
  path = null,
  noteContent = "",
  getSelection,
  onApply,
  onOpenNote,
  onDistill,
  onChatsChanged,
  initialChat = null,
  onInitialChatConsumed,
  onClose,
}) {
  const app = useContext(AppContext);
  const catalog = useModels();
  const [model, setModel] = useState(() => readPref("mn.model") || config?.default_model || "");
  const [scope, setScope] = useState(() => {
    const stored = readPref("mn.scope");
    return SCOPES.some((s) => s.value === stored) ? stored : "note";
  });
  const [prompts, setPrompts] = useState([]);
  const [messages, setMessages] = useState([]);
  const [chat, setChat] = useState(EMPTY_CHAT);
  const [input, setInput] = useState("");
  const [attached, setAttached] = useState("");
  const [busy, setBusy] = useState(false);
  const [loadingChat, setLoadingChat] = useState(false);
  const [error, setError] = useState(null); // {message, retry?, settings?, quiet?}
  const [historyOpen, setHistoryOpen] = useState(false);

  const messagesRef = useRef(messages);
  const chatRef = useRef(chat);
  const busyRef = useRef(false);
  const session = useRef(0);
  const abort = useRef(null);
  const saving = useRef(Promise.resolve());
  const scroller = useRef(null);
  const stick = useRef(true);
  const inputRef = useRef(null);
  const historyButton = useRef(null);
  const chips = useRef(null);
  const menuSelection = useRef("");

  const commit = (next) => {
    messagesRef.current = next;
    setMessages(next);
  };
  const setChatState = (next) => {
    chatRef.current = next;
    setChat(next);
  };
  const setBusyState = (value) => {
    busyRef.current = value;
    setBusy(value);
  };

  // ------------------------------------------------------------ preferences
  useEffect(() => {
    api
      .prompts()
      .then((r) => setPrompts(r.prompts || []))
      .catch(() => {});
  }, []);

  useEffect(() => {
    if (model) writePref("mn.model", model);
  }, [model]);
  useEffect(() => writePref("mn.scope", scope), [scope]);

  // The stored model is gone from the server's list: fall back to its default.
  useEffect(() => {
    const list = catalog.models;
    if (!list.length || list.includes(model)) return;
    setModel(catalog.default && list.includes(catalog.default) ? catalog.default : list[0]);
  }, [catalog.models, catalog.default]); // eslint-disable-line react-hooks/exhaustive-deps

  const modelOptions = useMemo(() => {
    const list = catalog.models.length ? catalog.models : model ? [model] : [];
    const groups = [];
    const options = list.map((id) => {
      const group = id.includes("/") ? id.split("/")[0] : undefined;
      if (group && !groups.includes(group)) groups.push(group);
      const label = catalog.labels[id];
      return { value: id, label: label || id, hint: label && label !== id ? id : undefined, group };
    });
    return options.sort((a, b) => groups.indexOf(a.group) - groups.indexOf(b.group));
  }, [catalog.models, catalog.labels, model]);

  // ------------------------------------------------------------ scrolling
  const onScroll = () => {
    const el = scroller.current;
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 64;
  };
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [messages, error]);

  // The composer grows with what is typed (up to ~7 lines).
  useLayoutEffect(() => {
    const el = inputRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 168)}px`;
  }, [input]);

  // Fades at the ends of the chip row while there is more to scroll to.
  const markChipEdges = () => {
    const el = chips.current;
    if (!el) return;
    el.dataset.start = el.scrollLeft > 2 ? "1" : "";
    el.dataset.end = el.scrollLeft + el.clientWidth >= el.scrollWidth - 2 ? "1" : "";
  };
  useEffect(() => {
    markChipEdges();
    const el = chips.current;
    if (!el || typeof ResizeObserver === "undefined") return undefined;
    const observer = new ResizeObserver(markChipEdges);
    observer.observe(el);
    return () => observer.disconnect();
  }, [prompts.length]);

  // ------------------------------------------------------------ saving
  /**
   * Saves the conversation to Chats/ (queued, so etags stay in order). Without
   * `force`, conversations of quick actions only stay unsaved until they have
   * been saved once. A conflict (the
   * file changed on disk, e.g. marked distilled) is retried on the new etag:
   * the transcript here is the newer one.
   */
  const persist = (all, { force = false } = {}) => {
    // The conversation this save is for. A queued save that only runs after
    // 新对话 or another chat was opened must not write this transcript into it.
    const id = session.current;
    const run = async () => {
      if (id !== session.current) return null;
      const finished = all.filter((m) => !m.pending);
      if (!finished.length) return chatRef.current;
      // Quick actions alone stay unsaved until the conversation is saved once (by hand, or by a typed question).
      if (!force && !chatRef.current.path && !finished.some((m) => m.role === "user" && !m.action)) return chatRef.current;
      const current = chatRef.current;
      const body = {
        path: current.path,
        title: titleFrom(finished),
        model,
        messages: finished.map(({ role, content }) => ({ role, content })),
      };
      let saved;
      try {
        saved = await api.saveChat({ ...body, base_etag: current.etag });
      } catch (err) {
        if (!(err instanceof ApiError) || err.status !== 409 || err.code === "no_vault" || !current.path) throw err;
        const fresh = await api.loadChat(current.path);
        saved = await api.saveChat({ ...body, base_etag: fresh.etag });
      }
      if (id !== session.current) return null;
      const next = {
        path: saved.path,
        etag: saved.etag,
        title: saved.meta?.title || body.title,
        status: saved.meta?.status || current.status || "inbox",
      };
      setChatState(next);
      onChatsChanged?.();
      return next;
    };
    saving.current = saving.current.then(run, run);
    return saving.current;
  };

  // ------------------------------------------------------------ sending
  /**
   * Sends a question and streams the answer.
   *
   * @param {object} userMessage {role:"user", content, action?, display?, source?}
   * @param {{scope?: string, selection?: string, history?: Array}} [options]
   */
  const send = async (userMessage, options = {}) => {
    if (busyRef.current) return;
    const requestScope = options.scope ?? scope;
    const selection = options.selection ?? attached;
    const history = options.history ?? messagesRef.current;
    if (config && config.ai_configured === false) {
      setError({ message: "还没有配置 AI 模型。", settings: true });
      return;
    }
    const id = session.current;
    const question = { ...userMessage, request: { scope: requestScope, selection } };
    const base = [...history, question];
    const thinking = THINKING[requestScope] || THINKING.none;
    setError(null);
    stick.current = true;
    commit([...base, { role: "assistant", content: "", pending: true, thinking, source: userMessage.source }]);
    setBusyState(true);
    setAttached("");
    const controller = new AbortController();
    abort.current = controller;
    let answer = "";
    let failure = null;
    try {
      await stream(
        "/api/ai/chat",
        {
          model,
          scope: requestScope,
          path,
          selection: selection || null,
          messages: base.map(({ role, content }) => ({ role, content })),
        },
        (event) => {
          if (event.error) throw new Error(event.error);
          if (event.delta) {
            answer += event.delta;
            if (id === session.current) commit([...base, { role: "assistant", content: answer, pending: true, thinking, source: userMessage.source }]);
          }
        },
        controller.signal,
      );
    } catch (err) {
      if (err.name !== "AbortError") failure = err;
    }
    if (abort.current === controller) abort.current = null;
    if (id !== session.current) return; // the conversation was switched meanwhile
    setBusyState(false);
    const stopped = controller.signal.aborted;
    if (!answer) {
      // Nothing came back: keep the question and offer to ask again.
      commit(base);
      setError(stopped ? { message: "已停止。", retry: true, quiet: true } : { message: failure?.message || "没有收到回复。", retry: true });
      return;
    }
    const final = [...base, { role: "assistant", content: answer, source: userMessage.source, stopped: stopped || undefined, failed: failure ? true : undefined }];
    commit(final);
    if (failure) setError({ message: failure.message, retry: true });
    persist(final).catch((err) => setError({ message: `对话保存失败：${err.message}` }));
  };

  /** Asks the last question again (after an error, or to regenerate the last answer). */
  const retry = () => {
    const list = messagesRef.current;
    let cut = list.length;
    while (cut > 0 && list[cut - 1].role === "assistant") cut -= 1;
    const question = list[cut - 1];
    if (!question || question.role !== "user") return;
    send(question, { history: list.slice(0, cut - 1), scope: question.request?.scope, selection: question.request?.selection ?? "" });
  };

  const stop = () => abort.current?.abort();

  const selected = () => menuSelection.current || getSelection?.() || "";

  const runAction = (action) => {
    const selection = selected();
    menuSelection.current = "";
    const whole = action.whole || !selection;
    const text = whole ? noteContent : selection;
    if (!text?.trim()) {
      setError({ message: path ? "这篇笔记还是空的，先写点内容或选中一段文本。" : "先打开一篇笔记，或选中一段文本。", quiet: true });
      return;
    }
    send(
      {
        role: "user",
        action: true,
        actionLabel: action.label,
        actionScope: whole ? "整篇笔记" : `选中 ${selection.length} 字`,
        display: `✦ ${action.label}（${whole ? "整篇笔记" : `选中 ${selection.length} 字`}）`,
        content: `${action.prompt}\n\n<<<\n${text}\n>>>`,
        source: { apply: action.apply, original: whole ? null : selection },
      },
      { scope: whole ? "none" : "note", selection: "" },
    );
  };

  const runPrompt = (prompt) => {
    const selection = selected();
    menuSelection.current = "";
    const text = selection || noteContent || "";
    const content = prompt.template.replaceAll("{activeNote}", noteContent || "").replaceAll("{}", text);
    send(
      {
        role: "user",
        action: true,
        actionLabel: prompt.name,
        actionScope: selection ? `选中 ${selection.length} 字` : noteContent ? "整篇笔记" : "",
        display: `✦ ${prompt.name}`,
        content,
        source: { apply: selection ? "replace" : null, original: selection || null },
      },
      { scope: "none", selection: "" },
    );
  };

  // The Prompts page's 「在 AI 面板中使用」: run the template here (with the
  // latest note and selection), and say so with ack() so the page stops offering.
  const runPromptRef = useRef(runPrompt);
  runPromptRef.current = runPrompt;
  const events = app?.events;
  useEffect(
    () =>
      events?.on("ai:use-prompt", (offer) => {
        if (busyRef.current || !offer?.template) return;
        offer.ack?.();
        runPromptRef.current({ name: offer.name || "提示词", template: offer.template });
      }),
    [events],
  );

  const submit = (event) => {
    event?.preventDefault();
    const text = input.trim();
    if (!text || busyRef.current) return;
    send({ role: "user", content: text, attached: attached.length });
    setInput("");
  };

  const ask = (text, askScope) => send({ role: "user", content: text }, { scope: askScope, selection: "" });

  // ------------------------------------------------------------ chats
  const newChat = () => {
    stop();
    session.current += 1;
    setBusyState(false);
    commit([]);
    setChatState(EMPTY_CHAT);
    setError(null);
    inputRef.current?.focus();
  };

  const loadChat = async (chatPath) => {
    stop();
    const id = ++session.current;
    setBusyState(false);
    setHistoryOpen(false);
    setLoadingChat(true);
    try {
      const loaded = await api.loadChat(chatPath);
      if (id !== session.current) return;
      stick.current = true;
      commit(decorate(loaded.messages));
      setChatState({ path: loaded.path, etag: loaded.etag, title: loaded.meta?.title || "", status: loaded.meta?.status || "inbox" });
      setError(null);
    } catch (err) {
      if (id === session.current) setError({ message: `打开对话失败：${err.message}` });
    } finally {
      if (id === session.current) setLoadingChat(false);
    }
  };

  // Another page asked to show a saved chat here.
  useEffect(() => {
    if (!initialChat) return;
    loadChat(initialChat).finally(() => onInitialChatConsumed?.());
  }, [initialChat]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => () => abort.current?.abort(), []);

  const saveNow = async () => {
    try {
      const saved = await persist(messagesRef.current, { force: true });
      if (saved?.path) toast.ok("对话已保存到 Chats/");
    } catch (err) {
      setError({ message: `对话保存失败：${err.message}` });
    }
  };

  const distill = async () => {
    if (busyRef.current) return;
    try {
      const saved = chatRef.current.path ? chatRef.current : await persist(messagesRef.current, { force: true });
      if (saved?.path) onDistill?.(saved.path);
    } catch (err) {
      setError({ message: `对话保存失败：${err.message}` });
    }
  };

  const transcript = () =>
    messagesRef.current
      .filter((m) => !m.pending)
      .map((m) => `### ${m.role === "user" ? "🧑 我" : "🤖 AI"}\n\n${m.role === "user" && m.action ? m.display : m.content}`)
      .join("\n\n");

  const selectionNow = () => {
    const text = getSelection?.() || "";
    if (text) {
      setAttached(text);
      inputRef.current?.focus();
    } else toast("先在笔记里选中一段文字");
  };

  /** Links in answers: [[notes]] open in the workspace; in the desktop app web links open in the browser. */
  const onLink = (event) =>
    routeLinkClick(event, {
      onNavigate: (note, anchor) => (anchor && app ? app.openNote(note, { anchor }) : onOpenNote?.(note)),
    });

  // ------------------------------------------------------------ render
  const resolver = app?.resolver ?? null;
  const typed = messages.some((m) => m.role === "user" && !m.action);
  const title = chat.title || (messages.length ? titleFrom(messages) : "新对话");

  const moreItems = () =>
    [
      { label: "保存对话", icon: "download", hint: chat.path ? chat.path : "存到 Chats/", disabled: !messages.length || busy, onSelect: saveNow },
      chat.path && onOpenNote && { label: "打开对话原文", icon: "file-text", onSelect: () => onOpenNote(chat.path) },
      { label: "复制为 Markdown", icon: "copy", disabled: !messages.length, onSelect: () => copyText(transcript()).then((ok) => ok && toast.ok("已复制对话")) },
      onDistill && { divider: true },
      onDistill && { label: "沉淀到知识库", icon: "sparkles", hint: "让 AI 把这次对话整理成笔记", disabled: !messages.length || busy, onSelect: distill },
    ].filter(Boolean);

  const promptItems = () => {
    api
      .prompts()
      .then((r) => setPrompts(r.prompts || []))
      .catch(() => {});
    return [
      { heading: "我的提示词" },
      ...prompts.map((p) => ({ label: p.name, icon: "prompt", hint: oneLine(p.template).slice(0, 46) || undefined, onSelect: () => runPrompt(p) })),
      app && { divider: true },
      app && { label: "管理提示词…", icon: "settings", onSelect: () => app.go("prompts") },
    ].filter(Boolean);
  };

  const byId = (id) => ACTIONS.find((a) => a.id === id);
  const suggestions = path
    ? [
        { icon: "list", text: "总结这篇笔记的要点", run: () => runAction(byId("summary")) },
        { icon: "graph", text: "为它画一张图解", run: () => runAction(byId("diagram")) },
        { icon: "check-circle", text: "出 5 道题自测一下", run: () => runAction(byId("quiz")) },
      ]
    : [
        { icon: "book-open", text: "我的知识库里都有哪些主题？", run: () => ask("我的知识库里都有哪些主题？按领域分组列出，并各举几篇代表笔记。", "vault") },
        { icon: "layers", text: "有哪些笔记值得合并或补充？", run: () => ask("通读我的知识库，指出内容重复、值得合并或明显缺内容需要补充的笔记，给出具体建议。", "vault") },
        { icon: "sparkles", text: "从我的笔记里出几道复习题", run: () => ask("从我的知识库里挑几个重要概念，出 5 道复习题，答案放在 Obsidian 折叠 callout（> [!note]- 答案）中。", "vault") },
      ];

  const lastIndex = messages.length - 1;

  return (
    <section className={`ai-panel ${embedded ? "embedded" : ""}`.trim()} aria-label="AI 助手">
      <header className="ai-head">
        <div className="ai-head-title">
          {!embedded && <Icon name="sparkles" size={14} className="ai-head-mark" />}
          <b title={chat.path || title}>{title}</b>
          {chat.path && (
            <Pill tone={chat.status === "distilled" ? "green" : "accent"} className="ai-status" title={chat.status === "distilled" ? "已沉淀到知识库" : "等待整理进知识库"}>
              {chat.status === "distilled" ? "已整理" : "待整理"}
            </Pill>
          )}
        </div>
        {onDistill && typed && (
          <Button size="sm" variant="soft" icon="sparkles" onClick={distill} disabled={busy} title="让 AI 把这次对话整理进知识库">
            沉淀
          </Button>
        )}
        {!typed && !chat.path && messages.length > 0 && (
          // Quick actions alone are not saved by themselves; keep this one on purpose.
          <IconButton icon="download" size="sm" label="保存对话到 Chats/" onClick={saveNow} disabled={busy} />
        )}
        <IconButton ref={historyButton} icon="history" size="sm" label="历史对话" active={historyOpen} onClick={() => setHistoryOpen((v) => !v)} />
        <IconButton icon="edit" size="sm" label="新对话" onClick={newChat} disabled={!messages.length && !chat.path && !error} />
        <MenuButton items={moreItems} label="更多" size="sm" />
        {onClose && <IconButton icon="x" size="sm" label="关闭" onClick={onClose} />}
      </header>

      <ChatHistory
        open={historyOpen}
        anchor={historyButton.current}
        current={chat.path}
        onClose={() => setHistoryOpen(false)}
        onPick={loadChat}
        onAll={app ? () => (setHistoryOpen(false), app.go("chats")) : null}
      />

      <div className={`ai-scroll ${loadingChat ? "is-loading" : ""}`.trim()} ref={scroller} onScroll={onScroll}>
        {messages.length === 0 && !loadingChat ? (
          <div className="ai-intro">
            <span className="ai-intro-mark">
              <Icon name="sparkles" size={17} />
            </span>
            <b>{path ? "问问这篇笔记" : "有什么想问的？"}</b>
            <p>直接提问，或对当前笔记、选中的文本用下面的快捷操作。</p>
            <div className="ai-suggest">
              {suggestions.map((s) => (
                <button key={s.text} type="button" disabled={busy} onMouseDown={(e) => e.preventDefault()} onClick={s.run}>
                  <Icon name={s.icon} size={14} />
                  <span>{s.text}</span>
                  <Icon name="arrow-right" size={12} className="ai-suggest-go" />
                </button>
              ))}
            </div>
            <p className="ai-intro-foot">
              有价值的对话会自动保存到 <code>Chats/</code>，之后点「沉淀」让 AI 把它整理成笔记。
            </p>
          </div>
        ) : (
          <div className="ai-thread">
            {messages.map((m, i) =>
              m.role === "user" ? (
                <UserMessage key={i} message={m} />
              ) : (
                <AssistantMessage
                  key={i}
                  message={m}
                  last={i === lastIndex}
                  busy={busy}
                  onApply={onApply}
                  onSaveNote={() => onApply?.({ type: "newNote", text: m.content, title: titleFrom(messages.slice(0, i)) })}
                  onRegenerate={retry}
                  resolver={resolver}
                  notePath={path}
                  onLink={onLink}
                />
              ),
            )}
          </div>
        )}
        {loadingChat && (
          <div className="ai-loading">
            <Spinner size={13} /> 正在打开对话…
          </div>
        )}
        {error && (
          <div className={`ai-error ${error.quiet ? "quiet" : ""}`.trim()} role={error.quiet ? "status" : "alert"}>
            <Icon name={error.quiet ? "info" : "alert-circle"} size={14} />
            <span className="ai-error-text">{error.message}</span>
            {error.retry && (
              <Button size="sm" variant="ghost" icon="refresh" onClick={retry} disabled={busy}>
                重试
              </Button>
            )}
            {error.settings && (
              <Button size="sm" variant="ghost" icon="settings" onClick={() => app.navigate("#!/settings/ai")}>
                去设置
              </Button>
            )}
            <IconButton icon="x" size="sm" label="知道了" onClick={() => setError(null)} />
          </div>
        )}
      </div>

      <div className="ai-dock">
        <div
          className="ai-chips"
          ref={chips}
          role="toolbar"
          aria-label="快捷操作"
          onScroll={markChipEdges}
          onWheel={(event) => {
            if (Math.abs(event.deltaY) > Math.abs(event.deltaX)) event.currentTarget.scrollLeft += event.deltaY;
          }}
        >
          {prompts.length > 0 && (
            <span
              className="ai-chip-wrap"
              onMouseDown={(event) => {
                // Opening the menu moves focus; remember the selection it is for first.
                menuSelection.current = getSelection?.() || "";
                event.preventDefault();
              }}
            >
              <MenuButton items={promptItems} label="我的提示词" placement="top-start" buttonClassName="ai-chip ai-chip-menu" width={290} disabled={busy}>
                <Icon name="prompt" size={12} />
                <span>提示词</span>
                <Icon name="chevron-down" size={11} />
              </MenuButton>
            </span>
          )}
          {GROUPS.map((group, gi) => (
            <Fragment key={group}>
              {(gi > 0 || prompts.length > 0) && <i className="ai-chips-sep" aria-hidden="true" />}
              {ACTIONS.filter((a) => a.group === group).map((a) => (
                <button key={a.id} type="button" className="ai-chip" disabled={busy} title={actionTitle(a)} onMouseDown={(e) => e.preventDefault()} onClick={() => runAction(a)}>
                  {a.label}
                </button>
              ))}
            </Fragment>
          ))}
        </div>

        <form className="ai-composer" onSubmit={submit}>
          <Segmented size="sm" fill value={scope} onChange={setScope} options={SCOPES} ariaLabel="对话上下文" className="ai-scope" />
          {attached && (
            <div className="ai-attached">
              <Icon name="at-sign" size={12} />
              <span className="ai-attached-text" title={attached}>
                {oneLine(attached)}
              </span>
              <em>{attached.length} 字</em>
              <button type="button" aria-label="移除附带的选区" title="移除" onClick={() => setAttached("")}>
                <Icon name="x" size={11} />
              </button>
            </div>
          )}
          <textarea
            ref={inputRef}
            className="ai-input"
            rows={2}
            value={input}
            placeholder={scope === "vault" ? "问问整个知识库…" : scope === "note" && path ? "问问这篇笔记…（Enter 发送，Shift+Enter 换行）" : "问点什么…（Enter 发送，Shift+Enter 换行）"}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (isComposingKey(e)) return;
              if (e.key === "Enter" && !e.shiftKey) submit(e);
              else if (e.key === "Escape" && busyRef.current) {
                e.preventDefault();
                stop();
              }
            }}
          />
          <div className="ai-bar">
            <Select
              size="sm"
              className="ai-model"
              value={model}
              onChange={setModel}
              options={modelOptions}
              searchable
              placeholder={catalog.loading ? "加载模型…" : "选择模型"}
              searchPlaceholder="搜索模型"
              menuWidth={300}
              prefix={<Icon name="cpu" size={12} />}
              ariaLabel="模型"
              title={model ? `模型：${catalog.labels[model] || model}` : "选择模型"}
            />
            {catalog.error && (
              <span className="ai-model-warn" title={`模型列表读取失败：${catalog.error}`}>
                <Icon name="alert" size={12} />
              </span>
            )}
            <span className="grow" />
            <IconButton icon="at-sign" size="sm" label="附带编辑器中选中的文本" onMouseDown={(e) => e.preventDefault()} onClick={selectionNow} />
            {busy ? (
              <button type="button" className="ai-send stop" onClick={stop} aria-label="停止生成" title="停止（Esc）">
                <i />
              </button>
            ) : (
              <button type="submit" className="ai-send" disabled={!input.trim()} aria-label="发送" title="发送（Enter）">
                <Icon name="arrow-up" size={14} stroke={2} />
              </button>
            )}
          </div>
        </form>
      </div>
    </section>
  );
}
