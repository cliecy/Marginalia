package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"marginalia/internal/knowledge"
	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
)

const (
	maxChatToolRounds = 8
	chatListLimit     = 200
)

// TurnStreamer streams a single assistant turn with optional function tools.
// *Client implements it.
type TurnStreamer interface {
	StreamTurn(ctx context.Context, model string, messages []ToolMessage, tools []ToolDef, onDelta func(string) error) (TurnResult, error)
}

// ChatEvent is one SSE payload emitted by POST /api/ai/chat.
type ChatEvent struct {
	Delta    string    `json:"delta,omitempty"`
	Status   string    `json:"status,omitempty"`
	Proposal *Proposal `json:"proposal,omitempty"`
	Done     bool      `json:"done,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// ChatRequest describes a chat completion request against a vault.
type ChatRequest struct {
	Model     string
	History   []Message
	Scope     string
	Path      string
	Selection string
	Tools     *bool
}
// PreparedChat holds the assembled messages and tool configuration ready to
// stream via Run. Calling PrepareChat before writing SSE headers lets HTTP
// handlers return pre-stream validation errors (e.g. 404) as JSON.
type PreparedChat struct {
	v            *vault.Vault
	model        string
	toolsEnabled bool
	plainMsgs    []ToolMessage
	msgs         []ToolMessage
	tools        []ToolDef
}

// PrepareChat validates chat context (such as reading req.Path under ScopeNote)
// and builds the initial messages and tool configuration.
func PrepareChat(v *vault.Vault, budget int, req ChatRequest) (*PreparedChat, error) {
	baseMsgs, err := BuildChatMessages(v, budget, req.History, req.Scope, req.Path, req.Selection)
	if err != nil {
		return nil, err
	}

	scope := req.Scope
	if scope == "" {
		scope = ScopeNote
	}
	toolsEnabled := scope != ScopeNone
	if req.Tools != nil {
		toolsEnabled = *req.Tools
	}

	plainMsgs := toToolMessages(baseMsgs, "")
	msgs := plainMsgs
	var tools []ToolDef
	if toolsEnabled {
		msgs = toToolMessages(baseMsgs, ChatToolsPrompt)
		tools = chatToolDefs()
	}

	return &PreparedChat{
		v:            v,
		model:        req.Model,
		toolsEnabled: toolsEnabled,
		plainMsgs:    plainMsgs,
		msgs:         msgs,
		tools:        tools,
	}, nil
}

// RunChat runs a streaming chat conversation, automatically executing read-only
// vault tools and staging write tools as reviewable Proposal events.
//
// Pre-stream context validation errors (e.g. missing focus note under
// ScopeNote) are returned before emit is called.
func RunChat(ctx context.Context, llm TurnStreamer, v *vault.Vault, budget int, req ChatRequest, emit func(ChatEvent) error) error {
	p, err := PrepareChat(v, budget, req)
	if err != nil {
		return err
	}
	return p.Run(ctx, llm, emit)
}

// Run executes the prepared chat session against llm and streams ChatEvents to emit.
func (p *PreparedChat) Run(ctx context.Context, llm TurnStreamer, emit func(ChatEvent) error) error {
	ws := newChatWorkspace(p.v)
	msgs := append([]ToolMessage(nil), p.msgs...)
	tools := p.tools
	toolsEnabled := p.toolsEnabled

	var emitted bool
	var emittedText strings.Builder

	emitChecked := func(ev ChatEvent) error {
		emitted = true
		return emit(ev)
	}
	deltaHandler := func(needSep bool) func(string) error {
		return func(delta string) error {
			if delta == "" {
				return nil
			}
			if needSep {
				needSep = false
				if !strings.HasPrefix(delta, "\n\n") {
					emittedText.WriteString("\n\n")
					if err := emitChecked(ChatEvent{Delta: "\n\n"}); err != nil {
						return err
					}
				}
			}
			emittedText.WriteString(delta)
			return emitChecked(ChatEvent{Delta: delta})
		}
	}

	hitToolLimit := false
	for round := range maxChatToolRounds {
		needSep := round > 0 && emittedText.Len() > 0 && !strings.HasSuffix(emittedText.String(), "\n\n")
		onDelta := deltaHandler(needSep)

		res, err := llm.StreamTurn(ctx, p.model, msgs, tools, onDelta)
		if err != nil {
			if toolsEnabled && round == 0 && !emitted && isToolUnsupportedErr(err) {
				toolsEnabled = false
				tools = nil
				msgs = p.plainMsgs
				_, err = llm.StreamTurn(ctx, p.model, msgs, nil, onDelta)
				if err != nil {
					return err
				}
				break
			}
			return err
		}

		if !toolsEnabled || len(res.ToolCalls) == 0 {
			break
		}

		var contentPtr *string
		if res.Content != "" {
			c := res.Content
			contentPtr = &c
		}
		msgs = append(msgs, ToolMessage{
			Role:      RoleAssistant,
			Content:   contentPtr,
			ToolCalls: res.ToolCalls,
		})

		for _, tc := range res.ToolCalls {
			if err := emitChecked(ChatEvent{Status: ws.statusFor(tc)}); err != nil {
				return err
			}
			out := ws.exec(tc)
			msgs = append(msgs, ToolMessage{
				Role:       RoleTool,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    &out,
			})
		}

		if round == maxChatToolRounds-1 {
			hitToolLimit = true
		}
	}

	if hitToolLimit {
		onDelta := deltaHandler(emittedText.Len() > 0 && !strings.HasSuffix(emittedText.String(), "\n\n"))
		if _, err := llm.StreamTurn(ctx, p.model, msgs, nil, onDelta); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !isToolUnsupportedErr(err) {
				return err
			}
			if err := onDelta("已达到单轮工具调用上限，请查看下方的修改建议或继续提问。"); err != nil {
				return err
			}
		}
	}

	for _, prop := range ws.proposals() {
		if err := emitChecked(ChatEvent{Proposal: prop}); err != nil {
			return err
		}
	}
	return nil
}

func toToolMessages(msgs []Message, extraSystem string) []ToolMessage {
	out := make([]ToolMessage, 0, len(msgs))
	for i, m := range msgs {
		content := m.Content
		if i == 0 && m.Role == RoleSystem && extraSystem != "" {
			content = content + "\n\n" + extraSystem
		}
		out = append(out, textToolMessage(m.Role, content))
	}
	return out
}

func isToolUnsupportedErr(err error) bool {
	var aiErr *Error
	if !errors.As(err, &aiErr) {
		return false
	}
	return aiErr.StatusCode == http.StatusBadRequest || aiErr.StatusCode == http.StatusUnprocessableEntity
}

// ------------------------------------------------------------------ tool defs

func chatToolDefs() []ToolDef {
	strProp := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	boolProp := func(desc string) map[string]any {
		return map[string]any{"type": "boolean", "description": desc}
	}
	objSchema := func(props map[string]any, required ...string) map[string]any {
		req := required
		if req == nil {
			req = []string{}
		}
		return map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             req,
			"additionalProperties": false,
		}
	}
	fn := func(name, desc string, params map[string]any) ToolDef {
		return ToolDef{
			Type: "function",
			Function: ToolFunction{
				Name:        name,
				Description: desc,
				Parameters:  params,
			},
		}
	}
	return []ToolDef{
		fn("get_conventions",
			"读取知识库约定 "+knowledge.ConventionsFile+"（目录结构、frontmatter、写作规范）。",
			objSchema(map[string]any{}),
		),
		fn("list_notes",
			"列出知识库笔记的路径、状态、标题、摘要与标签。",
			objSchema(map[string]any{
				"include_chats": boolProp("是否包含 Chats/ 对话记录，默认 false"),
			}),
		),
		fn("search_notes",
			"全文搜索笔记（空格分隔多个关键词，需全部匹配），返回路径和匹配行。",
			objSchema(map[string]any{
				"query": strProp("搜索关键词"),
			}, "query"),
		),
		fn("read_note",
			"读取一篇笔记的完整内容（若本次对话已暂存修改，则返回暂存后的最新内容）。",
			objSchema(map[string]any{
				"path": strProp("笔记相对路径，如 Knowledge/PPO.md"),
			}, "path"),
		),
		fn("get_backlinks",
			"列出反向链接到某篇笔记的其他笔记。",
			objSchema(map[string]any{
				"path": strProp("目标笔记路径"),
			}, "path"),
		),
		fn("edit_note",
			"对已有笔记或已暂存的新建笔记做精确文本替换，生成待用户确认的修改建议（不直接写入磁盘）。",
			objSchema(map[string]any{
				"path":     strProp("笔记路径（.md）"),
				"old_text": strProp("待替换的原文片段，必须在笔记中唯一出现"),
				"new_text": strProp("替换后的文本"),
				"reason":   strProp("可选：修改原因"),
			}, "path", "old_text", "new_text"),
		),
		fn("write_note",
			"创建新笔记或整体改写一篇笔记，生成待用户确认的修改建议（不直接写入磁盘）。局部修改请优先用 edit_note。",
			objSchema(map[string]any{
				"path":    strProp("笔记路径（.md）"),
				"content": strProp("笔记完整 Markdown 内容"),
				"reason":  strProp("可选：创建或改写原因"),
			}, "path", "content"),
		),
		fn("move_note",
			"建议移动或重命名笔记，生成待用户确认的移动建议（不直接修改磁盘）。",
			objSchema(map[string]any{
				"path":   strProp("原路径"),
				"to":     strProp("新路径"),
				"reason": strProp("可选：移动原因"),
			}, "path", "to"),
		),
		fn("delete_note",
			"建议删除一篇笔记，生成待用户确认的删除建议（不直接删除磁盘文件）。",
			objSchema(map[string]any{
				"path":   strProp("要删除的笔记路径"),
				"reason": strProp("可选：删除原因"),
			}, "path"),
		),
	}
}

// ------------------------------------------------------------ staged workspace

type chatWorkspace struct {
	v         *vault.Vault
	order     []string
	byPath    map[string]*Proposal
	movedFrom map[string]bool
	movedTo   map[string]string
}

func newChatWorkspace(v *vault.Vault) *chatWorkspace {
	return &chatWorkspace{
		v:         v,
		byPath:    make(map[string]*Proposal),
		movedFrom: make(map[string]bool),
		movedTo:   make(map[string]string),
	}
}

func (w *chatWorkspace) proposals() []*Proposal {
	out := make([]*Proposal, 0, len(w.order))
	for _, p := range w.order {
		if prop := w.byPath[p]; prop != nil {
			out = append(out, prop)
		}
	}
	return out
}

func (w *chatWorkspace) putProposal(p *Proposal) {
	if _, exists := w.byPath[p.Path]; !exists {
		w.order = append(w.order, p.Path)
	}
	w.byPath[p.Path] = p
}

func (w *chatWorkspace) removeProposal(path string) {
	delete(w.byPath, path)
	for i, p := range w.order {
		if p == path {
			w.order = append(w.order[:i], w.order[i+1:]...)
			break
		}
	}
}

func (w *chatWorkspace) statusFor(tc ToolCall) string {
	var raw map[string]any
	_ = json.Unmarshal([]byte(tc.Function.Arguments), &raw)
	strArg := func(key string) string {
		s, _ := raw[key].(string)
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		if clean, err := w.v.Clean(s); err == nil {
			return clean
		}
		return s
	}

	switch tc.Function.Name {
	case "get_conventions":
		return "正在读取知识库约定…"
	case "list_notes":
		return "正在浏览笔记目录…"
	case "search_notes":
		q, _ := raw["query"].(string)
		q = strings.TrimSpace(q)
		if q == "" {
			return "正在搜索笔记…"
		}
		return fmt.Sprintf("正在搜索「%s」…", pystr.Head(q, 24))
	case "read_note":
		if p := strArg("path"); p != "" {
			return "正在读取 " + p + "…"
		}
		return "正在读取笔记…"
	case "get_backlinks":
		if p := strArg("path"); p != "" {
			return "正在查找反向链接：" + p
		}
		return "正在查找反向链接…"
	case "edit_note", "write_note":
		if p := strArg("path"); p != "" {
			return "正在生成修改建议：" + p
		}
		return "正在生成修改建议…"
	case "move_note":
		src, dst := strArg("path"), strArg("to")
		if src != "" && dst != "" {
			return fmt.Sprintf("正在生成移动建议：%s → %s", src, dst)
		}
		return "正在生成移动建议…"
	case "delete_note":
		if p := strArg("path"); p != "" {
			return "正在生成删除建议：" + p
		}
		return "正在生成删除建议…"
	default:
		return "正在执行 " + tc.Function.Name + "…"
	}
}

func (w *chatWorkspace) exec(tc ToolCall) string {
	args, err := decodeToolArgs(tc.Function.Arguments)
	if err != nil {
		return toolErr(err)
	}
	var out string
	switch tc.Function.Name {
	case "get_conventions":
		out, err = w.getConventions()
	case "list_notes":
		out, err = w.listNotes(args)
	case "search_notes":
		out, err = w.searchNotes(args)
	case "read_note":
		out, err = w.readNote(args)
	case "get_backlinks":
		out, err = w.getBacklinks(args)
	case "edit_note":
		out, err = w.editNote(args)
	case "write_note":
		out, err = w.writeNote(args)
	case "move_note":
		out, err = w.moveNote(args)
	case "delete_note":
		out, err = w.deleteNote(args)
	default:
		return toolErr(fmt.Errorf("未知工具：%s", tc.Function.Name))
	}
	if err != nil {
		return toolErr(err)
	}
	return out
}

func toolErr(err error) string {
	return "工具调用失败：" + err.Error()
}

func decodeToolArgs(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return nil, fmt.Errorf("参数 JSON 无效：%w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func reqStr(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", fmt.Errorf("缺少必需参数 %s", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("参数 %s 必须是字符串", key)
	}
	return s, nil
}

func optStr(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("参数 %s 必须是字符串", key)
	}
	return s, nil
}

func optBool(args map[string]any, key string) (bool, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("参数 %s 必须是布尔值", key)
	}
	return b, nil
}

func isProtectedChatPath(clean string) bool {
	return clean == knowledge.ChatsDir ||
		strings.HasPrefix(clean, knowledge.ChatsDir+"/") ||
		clean == knowledge.ConventionsFile
}

func (w *chatWorkspace) cleanWritablePath(raw string, requireNote bool) (string, error) {
	clean, err := w.v.Clean(raw)
	if err != nil {
		return "", err
	}
	if isProtectedChatPath(clean) {
		return "", fmt.Errorf("禁止修改受保护文件：%s", clean)
	}
	if requireNote && !vault.IsNote(clean) {
		return "", fmt.Errorf("仅支持编辑 Markdown 笔记（.md）：%s", clean)
	}
	return clean, nil
}

// ------------------------------------------------------------ read tools

func (w *chatWorkspace) getConventions() (string, error) {
	note, err := w.v.ReadNote(knowledge.ConventionsFile)
	if err != nil {
		return "", err
	}
	return note.Content, nil
}

func (w *chatWorkspace) listNotes(args map[string]any) (string, error) {
	includeChats, err := optBool(args, "include_chats")
	if err != nil {
		return "", err
	}
	var lines []string
	total := 0
	for _, n := range knowledge.VaultIndex(w.v) {
		if !includeChats && n.Type == "chat" {
			continue
		}
		total++
		if len(lines) >= chatListLimit {
			continue
		}
		var b strings.Builder
		b.WriteString("- " + n.Path)
		if n.Status != "" {
			b.WriteString(" [" + n.Status + "]")
		}
		b.WriteString(" | " + n.Title + " | " + pystr.Head(n.Summary, 120))
		if len(n.Tags) > 0 {
			b.WriteString(" | #" + strings.Join(n.Tags, " #"))
		}
		lines = append(lines, b.String())
	}
	if len(lines) == 0 {
		return "（知识库为空）", nil
	}
	out := strings.Join(lines, "\n")
	if total > chatListLimit {
		out += fmt.Sprintf("\n…（共 %d 篇笔记，仅展示前 %d 篇；请用 search_notes 精确查找）", total, chatListLimit)
	}
	return out, nil
}

func (w *chatWorkspace) searchNotes(args map[string]any) (string, error) {
	query, err := reqStr(args, "query")
	if err != nil {
		return "", err
	}
	hits := w.v.Search(query, 30)
	if len(hits) == 0 {
		return "没有匹配的笔记", nil
	}
	blocks := make([]string, len(hits))
	for i, h := range hits {
		lines := make([]string, len(h.Snippets))
		for j, s := range h.Snippets {
			lines[j] = fmt.Sprintf("  L%d: %s", s.Line, s.Text)
		}
		blocks[i] = "## " + h.Path + "\n" + strings.Join(lines, "\n")
	}
	return strings.Join(blocks, "\n"), nil
}

func (w *chatWorkspace) readNote(args map[string]any) (string, error) {
	rawPath, err := reqStr(args, "path")
	if err != nil {
		return "", err
	}
	clean, err := w.v.Clean(rawPath)
	if err != nil {
		return "", err
	}
	if staged, ok := w.byPath[clean]; ok {
		switch staged.Op {
		case OpDelete:
			return "", fmt.Errorf("%s 已在本次对话中暂存删除（尚未写入磁盘）", clean)
		case OpMove:
			return "", fmt.Errorf("%s 已在本次对话中暂存移动到 %s（尚未写入磁盘）", clean, staged.To)
		case OpCreate, OpUpdate:
			return "path: " + clean + "\nstatus: staged\n\n" + clip(staged.After, focusNoteLimit), nil
		}
	}
	if src, ok := w.movedTo[clean]; ok {
		note, err := w.v.ReadNote(src)
		if err != nil {
			return "", err
		}
		return "path: " + clean + "\nstatus: staged (moved from " + src + ")\n\n" + clip(note.Content, focusNoteLimit), nil
	}
	note, err := w.v.ReadNote(clean)
	if err != nil {
		return "", err
	}
	return "path: " + note.Path + "\netag: " + note.ETag + "\n\n" + clip(note.Content, focusNoteLimit), nil
}

func (w *chatWorkspace) getBacklinks(args map[string]any) (string, error) {
	path, err := reqStr(args, "path")
	if err != nil {
		return "", err
	}
	links, err := w.v.Backlinks(path)
	if err != nil {
		return "", err
	}
	if len(links) == 0 {
		return "没有反向链接", nil
	}
	lines := make([]string, len(links))
	for i, b := range links {
		lines[i] = "- " + b.Path + ": " + strings.Join(b.Lines, " / ")
	}
	return strings.Join(lines, "\n"), nil
}

// ------------------------------------------------------------ write tools

func mergeReasons(prev, next string) string {
	prev = strings.TrimSpace(prev)
	next = strings.TrimSpace(next)
	switch {
	case next == "":
		return prev
	case prev == "" || prev == next:
		return next
	default:
		return prev + "；" + next
	}
}

func (w *chatWorkspace) editNote(args map[string]any) (string, error) {
	rawPath, err := reqStr(args, "path")
	if err != nil {
		return "", err
	}
	clean, err := w.cleanWritablePath(rawPath, true)
	if err != nil {
		return "", err
	}
	oldText, err := reqStr(args, "old_text")
	if err != nil {
		return "", err
	}
	newText, err := reqStr(args, "new_text")
	if err != nil {
		return "", err
	}
	reason, err := optStr(args, "reason")
	if err != nil {
		return "", err
	}
	if oldText == "" {
		return "", errors.New("old_text 不能为空")
	}
	if w.movedTo[clean] != "" {
		return "", fmt.Errorf("%s 是已暂存移动的目标路径，无法再编辑", clean)
	}

	var cur, before, prevReason string
	var baseETag *string
	var opKind string

	if staged, ok := w.byPath[clean]; ok {
		if staged.Op == OpMove || staged.Op == OpDelete {
			return "", fmt.Errorf("%s 已暂存移动或删除，无法编辑", clean)
		}
		opKind = staged.Op
		cur = staged.After
		before = staged.Before
		baseETag = staged.BaseETag
		prevReason = staged.Reason
	} else {
		note, err := w.v.ReadNote(clean)
		if err != nil {
			return "", err
		}
		opKind = OpUpdate
		cur = note.Content
		before = note.Content
		etag := note.ETag
		baseETag = &etag
	}

	count := strings.Count(cur, oldText)
	if count == 0 {
		return "", errors.New("未在笔记中找到 old_text，请先调用 read_note 查看最新内容后重试")
	}
	if count > 1 {
		return "", fmt.Errorf("old_text 在笔记中匹配到 %d 处，请提供更多上下文以唯一定位", count)
	}
	updated := strings.Replace(cur, oldText, newText, 1)
	if updated == cur {
		return "", errors.New("修改后内容无变化")
	}
	if opKind == OpUpdate && updated == before {
		w.removeProposal(clean)
		return "修改后与原笔记内容一致，已取消暂存修改", nil
	}

	w.putProposal(&Proposal{
		Op:       opKind,
		Path:     clean,
		Reason:   mergeReasons(prevReason, reason),
		Before:   before,
		BaseETag: baseETag,
		After:    updated,
	})
	return fmt.Sprintf("已暂存修改建议（%s，尚未写入磁盘，待用户确认应用）", clean), nil
}

func (w *chatWorkspace) writeNote(args map[string]any) (string, error) {
	rawPath, err := reqStr(args, "path")
	if err != nil {
		return "", err
	}
	clean, err := w.cleanWritablePath(rawPath, true)
	if err != nil {
		return "", err
	}
	rawContent, err := reqStr(args, "content")
	if err != nil {
		return "", err
	}
	reason, err := optStr(args, "reason")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(rawContent) == "" {
		return "", errors.New("content 不能为空")
	}
	if w.movedTo[clean] != "" {
		return "", fmt.Errorf("%s 是已暂存移动的目标路径，无法覆盖", clean)
	}
	if staged, ok := w.byPath[clean]; ok && (staged.Op == OpMove || staged.Op == OpDelete) {
		return "", fmt.Errorf("%s 已暂存移动或删除，无法写入", clean)
	}

	content := StripFence(rawContent)
	var before string
	var baseETag *string
	opKind := OpCreate

	note, err := w.v.ReadNote(clean)
	switch {
	case err == nil:
		if content == note.Content {
			if staged, ok := w.byPath[clean]; ok && staged.Op == OpUpdate {
				w.removeProposal(clean)
			}
			return "内容与原笔记相同，无需修改", nil
		}
		opKind = OpUpdate
		before = note.Content
		etag := note.ETag
		baseETag = &etag
	case errors.Is(err, vault.ErrNotFound):
		opKind = OpCreate
	default:
		return "", err
	}

	var prevReason string
	if staged, ok := w.byPath[clean]; ok {
		prevReason = staged.Reason
	}
	w.putProposal(&Proposal{
		Op:       opKind,
		Path:     clean,
		Reason:   mergeReasons(prevReason, reason),
		Before:   before,
		BaseETag: baseETag,
		After:    content,
	})
	label := "新建"
	if opKind == OpUpdate {
		label = "更新"
	}
	return fmt.Sprintf("已暂存%s建议（%s，尚未写入磁盘，待用户确认应用）", label, clean), nil
}

func (w *chatWorkspace) moveNote(args map[string]any) (string, error) {
	rawPath, err := reqStr(args, "path")
	if err != nil {
		return "", err
	}
	src, err := w.cleanWritablePath(rawPath, false)
	if err != nil {
		return "", err
	}
	rawTo, err := reqStr(args, "to")
	if err != nil {
		return "", err
	}
	dst, err := w.cleanWritablePath(rawTo, false)
	if err != nil {
		return "", err
	}
	reason, err := optStr(args, "reason")
	if err != nil {
		return "", err
	}
	if src == dst {
		return "", errors.New("源路径与目标路径相同")
	}
	if _, ok := w.byPath[src]; ok {
		return "", fmt.Errorf("%s 已有暂存操作，无法再移动", src)
	}
	if !exists(w.v, src) {
		return "", fmt.Errorf("源路径不存在：%s", src)
	}
	if exists(w.v, dst) || w.movedTo[dst] != "" || w.byPath[dst] != nil {
		return "", fmt.Errorf("目标路径已存在或已有暂存操作：%s", dst)
	}

	var before string
	var baseETag *string
	if note, err := w.v.ReadNote(src); err == nil {
		before = note.Content
		etag := note.ETag
		baseETag = &etag
	} else {
		var verr *vault.Error
		if !errors.As(err, &verr) {
			return "", err
		}
	}

	w.movedFrom[src] = true
	w.movedTo[dst] = src
	w.putProposal(&Proposal{
		Op:       OpMove,
		Path:     src,
		To:       dst,
		Reason:   strings.TrimSpace(reason),
		Before:   before,
		BaseETag: baseETag,
	})
	return fmt.Sprintf("已暂存移动建议（%s → %s，尚未写入磁盘，待用户确认应用）", src, dst), nil
}

func (w *chatWorkspace) deleteNote(args map[string]any) (string, error) {
	rawPath, err := reqStr(args, "path")
	if err != nil {
		return "", err
	}
	clean, err := w.cleanWritablePath(rawPath, false)
	if err != nil {
		return "", err
	}
	reason, err := optStr(args, "reason")
	if err != nil {
		return "", err
	}
	if _, ok := w.byPath[clean]; ok {
		return "", fmt.Errorf("%s 已有暂存操作，无法再删除", clean)
	}
	if w.movedTo[clean] != "" {
		return "", fmt.Errorf("%s 是已暂存移动的目标路径，无法删除", clean)
	}

	var before string
	var baseETag *string
	note, err := w.v.ReadNote(clean)
	switch {
	case err == nil:
		before = note.Content
		etag := note.ETag
		baseETag = &etag
	case exists(w.v, clean):
		var verr *vault.Error
		if !errors.As(err, &verr) {
			return "", err
		}
	default:
		return "", fmt.Errorf("找不到要删除的路径：%s", clean)
	}

	w.putProposal(&Proposal{
		Op:       OpDelete,
		Path:     clean,
		Reason:   strings.TrimSpace(reason),
		Before:   before,
		BaseETag: baseETag,
	})
	return fmt.Sprintf("已暂存删除建议（%s，尚未写入磁盘，待用户确认应用）", clean), nil
}
