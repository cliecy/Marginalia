package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marginalia/internal/chats"
	"marginalia/internal/knowledge"
	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
)

// ToolInfo describes a tool the way tools/list reports it.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations Annotations     `json:"annotations"`
}

// Annotations are the behaviour hints of a tool.
type Annotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
}

// tool is a registered tool: its public description, its parameters in
// signature order and its implementation.
type tool struct {
	info   ToolInfo
	params []param
	run    func(s *Server, v *vault.Vault, a *args) (string, error)
}

// param is one keyword parameter of a tool.
type param struct {
	name     string
	schema   object // JSON schema of the value
	required bool
}

// param returns t's parameter called name, or nil.
func (t *tool) param(name string) *param {
	for i := range t.params {
		if t.params[i].name == name {
			return &t.params[i]
		}
	}
	return nil
}

// Schema building blocks, mirroring mcp.py's STR and _schema().
func strSchema() object { return object{{"type", "string"}} }

func describedSchema(typ, description string) object {
	return object{{"type", typ}, {"description", description}}
}

func objectSchema(params []param) object {
	properties := object{}
	required := []string{}
	for _, p := range params {
		properties = append(properties, member{p.name, p.schema})
		if p.required {
			required = append(required, p.name)
		}
	}
	return object{{"type", "object"}, {"properties", properties}, {"required", required}, {"additionalProperties", false}}
}

// newTool assembles a registry entry. delete_note is the only destructive
// tool, as in Python.
func newTool(name, description string, readOnly bool, params []param, run func(*Server, *vault.Vault, *args) (string, error)) *tool {
	schema, err := json.Marshal(objectSchema(params))
	if err != nil {
		panic(fmt.Sprintf("mcp: schema of %s: %v", name, err)) // static data: a programming error
	}
	return &tool{
		info: ToolInfo{
			Name:        name,
			Description: description,
			InputSchema: schema,
			Annotations: Annotations{ReadOnlyHint: readOnly, DestructiveHint: name == "delete_note"},
		},
		params: params,
		run:    run,
	}
}

// registry lists the tools in tools/list order. It is built once and never
// modified.
var registry = []*tool{
	newTool("get_conventions",
		"读取知识库约定 "+knowledge.ConventionsFile+"（目录结构、frontmatter、写作规范）。写入前必须先读。",
		true, nil, getConventions),
	newTool("list_notes",
		"列出所有知识笔记的路径、标题、summary、标签和出链。用于了解知识库全貌。",
		true, []param{
			{name: "include_chats", schema: describedSchema("boolean", "是否包含 Chats/ 对话记录，默认 false")},
		}, listNotes),
	newTool("search_notes",
		"全文搜索笔记（空格分隔多个关键词，需全部匹配），返回路径和匹配行。",
		true, []param{
			{name: "query", schema: strSchema(), required: true},
		}, searchNotes),
	newTool("read_note",
		"读取一篇笔记的完整内容。返回内容与 etag（更新时可传回 etag 以防覆盖他人修改）。",
		true, []param{
			{name: "path", schema: describedSchema("string", "相对路径，如 Knowledge/机器学习/PPO.md"), required: true},
		}, readNote),
	newTool("get_backlinks",
		"列出链接到某篇笔记的其他笔记。",
		true, []param{
			{name: "path", schema: strSchema(), required: true},
		}, getBacklinks),
	newTool("write_note",
		"创建或整体覆盖一篇笔记（.md）。覆盖已有笔记时建议传入 read_note 得到的 etag。",
		false, []param{
			{name: "path", schema: strSchema(), required: true},
			{name: "content", schema: strSchema(), required: true},
			{name: "etag", schema: describedSchema("string", "可选：基于的版本")},
		}, writeNote),
	newTool("append_to_note",
		"在笔记末尾追加内容；笔记不存在时创建。",
		false, []param{
			{name: "path", schema: strSchema(), required: true},
			{name: "text", schema: strSchema(), required: true},
		}, appendToNote),
	newTool("move_note",
		"移动或重命名笔记/附件，并自动更新其他笔记中指向它的 [[双链]]。",
		false, []param{
			{name: "path", schema: strSchema(), required: true},
			{name: "to", schema: strSchema(), required: true},
		}, moveNote),
	newTool("delete_note",
		"删除一篇笔记或空文件夹（可从 git 历史恢复）。仅在内容已合并到别处时使用。",
		false, []param{
			{name: "path", schema: strSchema(), required: true},
		}, deleteNote),
	newTool("save_conversation",
		"把一段与用户的对话存入 Chats/ 收件箱（status: inbox），之后可在网页端「AI 整理」中沉淀为知识笔记。"+
			"messages 为 [{role: user|assistant, content}]。",
		false, []param{
			{name: "title", schema: strSchema(), required: true},
			{name: "messages", required: true, schema: object{
				{"type", "array"},
				{"items", objectSchema([]param{
					{name: "role", schema: object{{"type", "string"}, {"enum", []string{chats.RoleUser, chats.RoleAssistant}}}, required: true},
					{name: "content", schema: strSchema(), required: true},
				})},
			}},
			{name: "source", schema: describedSchema("string", "可选：来自哪个客户端/模型")},
		}, saveConversation),
	newTool("list_inbox",
		"列出 Chats/ 中尚未整理（status: inbox）的对话。",
		true, nil, listInbox),
	newTool("mark_chat_distilled",
		"把一个对话标记为已整理，并记录它被沉淀到了哪些笔记。",
		false, []param{
			{name: "path", schema: strSchema(), required: true},
			{name: "notes", schema: object{{"type", "array"}, {"items", strSchema()}}},
		}, markChatDistilled),
}

// lookup returns the tool called name (which must be a string), or nil.
func lookup(name any) *tool {
	s, ok := name.(string)
	if !ok {
		return nil
	}
	for _, t := range registry {
		if t.info.Name == s {
			return t
		}
	}
	return nil
}

// ------------------------------------------------------------ read tools

func getConventions(_ *Server, v *vault.Vault, _ *args) (string, error) {
	note, err := v.ReadNote(knowledge.ConventionsFile)
	if err != nil {
		return "", err
	}
	return note.Content, nil
}

func listNotes(_ *Server, v *vault.Vault, a *args) (string, error) {
	includeChats := a.flag("include_chats")
	var lines []string
	for _, n := range knowledge.VaultIndex(v) {
		if !includeChats && n.Type == "chat" {
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
	return strings.Join(lines, "\n"), nil
}

// searchLimit is the number of notes search_notes returns at most.
const searchLimit = 30

func searchNotes(_ *Server, v *vault.Vault, a *args) (string, error) {
	query, err := a.str("query")
	if err != nil {
		return "", err
	}
	hits := v.Search(query, searchLimit)
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

func readNote(_ *Server, v *vault.Vault, a *args) (string, error) {
	path, err := a.str("path")
	if err != nil {
		return "", err
	}
	note, err := v.ReadNote(path)
	if err != nil {
		return "", err
	}
	return "path: " + note.Path + "\netag: " + note.ETag + "\n\n" + note.Content, nil
}

func getBacklinks(_ *Server, v *vault.Vault, a *args) (string, error) {
	path, err := a.str("path")
	if err != nil {
		return "", err
	}
	links, err := v.Backlinks(path)
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

func listInbox(_ *Server, v *vault.Vault, _ *args) (string, error) {
	var lines []string
	for _, c := range chats.List(v) {
		if c.Status == chats.StatusInbox {
			lines = append(lines, "- "+c.Path+" | "+c.Title+" | "+pystr.Head(c.Updated, 16))
		}
	}
	if len(lines) == 0 {
		return "收件箱为空", nil
	}
	return strings.Join(lines, "\n"), nil
}

// ----------------------------------------------------------- write tools

func writeNote(s *Server, v *vault.Vault, a *args) (string, error) {
	path, err := a.str("path")
	if err != nil {
		return "", err
	}
	content, err := a.str("content")
	if err != nil {
		return "", err
	}
	etag, err := a.optStr("etag")
	if err != nil {
		return "", err
	}
	result, err := v.WriteNote(path, content, etag)
	if err != nil {
		return "", err
	}
	s.record([]string{result.Path}, "write "+result.Path)
	return fmt.Sprintf("已保存 %s（etag %s）", result.Path, result.ETag), nil
}

func appendToNote(s *Server, v *vault.Vault, a *args) (string, error) {
	path, err := a.str("path")
	if err != nil {
		return "", err
	}
	text, err := a.str("text")
	if err != nil {
		return "", err
	}
	var content string
	var etag *string
	switch note, err := v.ReadNote(path); {
	case err == nil:
		content = pystr.RStrip(note.Content) + "\n\n" + pystr.Strip(text) + "\n"
		etag = &note.ETag
	case errors.Is(err, vault.ErrNotFound):
		content = pystr.Strip(text) + "\n"
	default:
		return "", err
	}
	result, err := v.WriteNote(path, content, etag)
	if err != nil {
		return "", err
	}
	s.record([]string{result.Path}, "append "+result.Path)
	return "已追加到 " + result.Path, nil
}

func moveNote(s *Server, v *vault.Vault, a *args) (string, error) {
	path, err := a.str("path")
	if err != nil {
		return "", err
	}
	to, err := a.str("to")
	if err != nil {
		return "", err
	}
	s.git.Flush()
	result, err := v.Move(path, to, true)
	if err != nil {
		return "", err
	}
	src, err := v.Clean(path)
	if err != nil {
		return "", err // unreachable: Move accepted path
	}
	paths := append([]string{src, result.Path}, result.UpdatedLinksIn...)
	s.record(paths, "move "+path+" -> "+result.Path)
	return fmt.Sprintf("已移动到 %s，更新了 %d 篇笔记的链接", result.Path, len(result.UpdatedLinksIn)), nil
}

func deleteNote(s *Server, v *vault.Vault, a *args) (string, error) {
	path, err := a.str("path")
	if err != nil {
		return "", err
	}
	s.git.Flush()
	if err := v.Delete(path); err != nil {
		return "", err
	}
	rel, err := v.Clean(path)
	if err != nil {
		return "", err // unreachable: Delete accepted path
	}
	s.record([]string{rel}, "delete "+path)
	return "已删除 " + path, nil
}

// defaultSource is the chat "model" recorded when the client names none.
const defaultSource = "mcp"

func saveConversation(s *Server, v *vault.Vault, a *args) (string, error) {
	title, err := a.str("title")
	if err != nil {
		return "", err
	}
	messages, err := a.messages("messages")
	if err != nil {
		return "", err
	}
	source, err := a.optStr("source")
	if err != nil {
		return "", err
	}
	model := defaultSource
	if source != nil {
		model = *source
	}
	saved, err := chats.Save(v, "", title, model, messages, nil)
	if err != nil {
		return "", err
	}
	s.record([]string{saved.Path}, "chat "+title)
	return "已保存到 " + saved.Path, nil
}

func markChatDistilled(s *Server, v *vault.Vault, a *args) (string, error) {
	path, err := a.str("path")
	if err != nil {
		return "", err
	}
	notes, err := a.strList("notes")
	if err != nil {
		return "", err
	}
	rel, err := v.Clean(path)
	if err != nil {
		return "", err
	}
	if rel, err = chats.SetStatus(v, rel, chats.StatusDistilled, notes); err != nil {
		return "", err
	}
	s.record([]string{rel}, "distilled "+rel)
	return "已标记 " + rel + " 为已整理", nil
}
