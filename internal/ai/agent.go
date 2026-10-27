package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"marginalia/internal/knowledge"
	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
)

// Completer produces a complete chat answer. *Client implements it; tests
// and other callers may substitute their own.
type Completer interface {
	Complete(ctx context.Context, model string, messages []Message) (string, error)
}

// Operation kinds of a changeset.
const (
	OpCreate = "create"
	OpUpdate = "update"
	OpMove   = "move"
	OpDelete = "delete"
)

// MaxAgentOperations caps how many planned operations become proposals.
const MaxAgentOperations = 15

// Event kinds streamed by POST /api/ai/agent (the "event" member).
const (
	EventStatus   = "status"   // progress line: Message
	EventPlan     = "plan"     // the plan was made: Summary, Count
	EventProposal = "proposal" // one reviewable change: Proposal
	EventDone     = "done"     // finished: Summary, Chats
	EventError    = "error"    // failed (sent by the HTTP layer): Message
)

// Event is one agent progress event. Its JSON form depends on Kind:
//
//	{"event":"status","message":"…"}
//	{"event":"plan","summary":"…","count":3}
//	{"event":"proposal","proposal":{…Proposal…}}
//	{"event":"done","summary":"…","chats":["Chats/…md"]}
//	{"event":"error","message":"…"}
type Event struct {
	Kind     string
	Message  string    // EventStatus, EventError
	Summary  string    // EventPlan, EventDone: the plan's one-line summary
	Count    int       // EventPlan: number of planned operations
	Proposal *Proposal // EventProposal
	Chats    []string  // EventDone: the chats the run was about
}

// ErrorEvent is the event the HTTP layer sends when RunAgent fails.
func ErrorEvent(err error) Event { return Event{Kind: EventError, Message: err.Error()} }

// MarshalJSON encodes the event in the shape the frontend expects (see Event).
func (e Event) MarshalJSON() ([]byte, error) {
	switch e.Kind {
	case EventStatus, EventError:
		return marshalJSON(struct {
			Event   string `json:"event"`
			Message string `json:"message"`
		}{e.Kind, e.Message})
	case EventPlan:
		return marshalJSON(struct {
			Event   string `json:"event"`
			Summary string `json:"summary"`
			Count   int    `json:"count"`
		}{e.Kind, e.Summary, e.Count})
	case EventProposal:
		return marshalJSON(struct {
			Event    string    `json:"event"`
			Proposal *Proposal `json:"proposal"`
		}{e.Kind, e.Proposal})
	case EventDone:
		chats := e.Chats
		if chats == nil {
			chats = []string{}
		}
		return marshalJSON(struct {
			Event   string   `json:"event"`
			Summary string   `json:"summary"`
			Chats   []string `json:"chats"`
		}{e.Kind, e.Summary, chats})
	}
	return nil, fmt.Errorf("ai: unknown event kind %q", e.Kind)
}

// Proposal is one change the agent proposes, and (minus Before) one
// operation of POST /api/ai/apply.
type Proposal struct {
	Op     string `json:"op"`     // OpCreate, OpUpdate, OpMove or OpDelete
	Path   string `json:"path"`   // vault-relative, cleaned
	Reason string `json:"reason"` // why, in the model's words
	// Before is the note's content when the proposal was made ("" for new
	// notes, folders and other files). The frontend shows it in the diff and
	// does not send it back.
	Before string `json:"before"`
	// BaseETag is the note's ETag when the proposal was made, or nil (JSON
	// null) when it did not exist or is not a text file. ApplyChangeset
	// refuses to touch a note whose ETag changed since.
	BaseETag *string `json:"base_etag"`
	To       string  `json:"to,omitempty"`    // OpMove: destination
	After    string  `json:"after,omitempty"` // OpCreate/OpUpdate: the new content
}

// AgentRequest is the body of POST /api/ai/agent.
type AgentRequest struct {
	Model string   // "" selects the client's default model
	Task  string   // what to do, in natural language
	Chats []string // chats to distil, already cleaned with vault.Clean
	Path  string   // the note open in the editor ("" = none); ranked first in the context
}

// RunAgent plans and drafts a changeset for req.Task and reports progress
// through emit. Nothing is written to the vault: every change arrives as an
// EventProposal for the user to review and pass to ApplyChangeset.
//
// The run makes one planning request (PlanPrompt) and one writing request
// (WritePrompt) per note to create or update. Events, in order: EventStatus,
// EventPlan, then per operation an EventStatus before each note is written
// and an EventProposal for each change kept, and finally EventDone. Planned
// operations are dropped when their op is unknown, their path (or move
// destination) is invalid, a chat or the conventions file, when a move
// source or delete target does not exist, when a note would be written to a
// non-note path, or when a rewrite leaves a note unchanged; create and update
// are corrected to match whether the note exists. Only the first
// MaxAgentOperations operations are considered.
//
// budget is the context size in characters (config AISettings.VaultCharBudget);
// with chats to distil, half of it goes to the chats and half to the vault.
//
// RunAgent returns the first error of the model (an *Error, or ctx.Err()),
// of the plan (an *Error from ExtractJSON), of emit (returned unchanged, e.g.
// when the client went away), or of reading the vault. Events already
// emitted stay valid; the HTTP layer reports the error with ErrorEvent.
func RunAgent(ctx context.Context, llm Completer, v *vault.Vault, budget int, req AgentRequest, emit func(Event) error) error {
	chats := req.Chats
	if chats == nil {
		chats = []string{}
	}
	// Python emitted this after assembling the context; sending it first only
	// shows progress sooner.
	if err := emit(Event{Kind: EventStatus, Message: "正在分析笔记库并制定修改计划…"}); err != nil {
		return err
	}

	rules := Conventions(v)
	if rules == "" {
		rules = "（暂无约定文件）"
	}
	sources := sourceMaterial(v, chats, budget/2)
	contextBudget := budget
	if sources != "" {
		contextBudget = budget / 2
	}
	var focus []string
	if req.Path != "" {
		focus = []string{req.Path}
	}
	shared := "今天是 " + today() + "。\n\n## 知识库约定\n" + clip(rules, conventionsAgentLimit) +
		"\n\n## 笔记库\n" + VaultContext(v, contextBudget, focus, req.Task, false)
	if sources != "" {
		shared += "\n\n## 待整理的原始对话\n" + sources
	}

	planText, err := llm.Complete(ctx, req.Model, []Message{
		{Role: RoleSystem, Content: PlanPrompt},
		{Role: RoleUser, Content: shared + "\n\n## 任务\n" + req.Task},
	})
	if err != nil {
		return err
	}
	plan, err := ExtractJSON(planText)
	if err != nil {
		return err
	}
	operations := planOperations(plan)
	summary := get(plan, "summary", "")
	if err := emit(Event{Kind: EventPlan, Summary: text(summary), Count: len(operations)}); err != nil {
		return err
	}

	// The list of planned operations every writing request sees.
	var overview strings.Builder
	overview.WriteString("\n\n## 本次整体任务\n" + req.Task + "\n\n## 本次修改计划\n" + pyyaml.Str(summary) + "\n")
	for i, op := range operations {
		if i > 0 {
			overview.WriteString("\n")
		}
		overview.WriteString("- " + pyyaml.Str(op.Value("op")) + " " + pyyaml.Str(op.Value("path")))
	}

	for index, op := range operations[:min(len(operations), MaxAgentOperations)] {
		proposal, err := propose(v, op)
		if err != nil {
			return err
		}
		if proposal == nil {
			continue
		}
		if proposal.Op == OpCreate || proposal.Op == OpUpdate {
			status := fmt.Sprintf("（%d/%d）正在撰写 %s…", index+1, len(operations), proposal.Path)
			if err := emit(Event{Kind: EventStatus, Message: status}); err != nil {
				return err
			}
			existing := "## 这是一篇新笔记"
			if proposal.BaseETag != nil {
				existing = "## 这篇笔记的当前内容\n" + proposal.Before
			}
			instructions := pyyaml.Str(pyyaml.Or(op.Value("instructions"), get(op, "reason", "")))
			answer, err := llm.Complete(ctx, req.Model, []Message{
				{Role: RoleSystem, Content: WritePrompt},
				{Role: RoleUser, Content: shared + overview.String() +
					"\n\n## 你要写的笔记：" + proposal.Path + "\n要求：" + instructions + "\n\n" + existing},
			})
			if err != nil {
				return err
			}
			proposal.After = StripFence(answer)
			if proposal.BaseETag != nil && pystr.Strip(proposal.After) == pystr.Strip(proposal.Before) {
				continue
			}
		}
		if err := emit(Event{Kind: EventProposal, Proposal: proposal}); err != nil {
			return err
		}
	}
	return emit(Event{Kind: EventDone, Summary: text(summary), Chats: chats})
}

// propose validates one planned operation against the vault and returns its
// proposal without After, or nil when the operation is to be dropped.
func propose(v *vault.Vault, op *pyyaml.Map) (*Proposal, error) {
	kind, _ := op.Value("op").(string)
	path, err := v.Clean(pyyaml.Str(get(op, "path", "")))
	if err != nil {
		return nil, nil
	}
	if strings.HasPrefix(path, knowledge.ChatsDir+"/") || path == knowledge.ConventionsFile {
		return nil, nil
	}
	current, err := v.ReadNote(path)
	if err != nil {
		var verr *vault.Error
		if !errors.As(err, &verr) {
			return nil, err // I/O failure: Python let it end the run too
		}
		current = nil
	}
	p := &Proposal{Op: kind, Path: path, Reason: text(get(op, "reason", ""))}
	if current != nil {
		etag := current.ETag
		p.Before, p.BaseETag = current.Content, &etag
	}

	switch kind {
	case OpMove:
		to, err := v.Clean(pyyaml.Str(get(op, "to", "")))
		if err != nil {
			return nil, nil
		}
		p.To = to
		if current == nil && !exists(v, path) {
			return nil, nil
		}
	case OpDelete:
		if current == nil {
			return nil, nil
		}
	case OpCreate, OpUpdate:
		if !vault.IsNote(path) {
			return nil, nil
		}
		p.Op = OpCreate
		if current != nil {
			p.Op = OpUpdate
		}
	default:
		return nil, nil
	}
	return p, nil
}

// exists reports whether rel names an existing file or folder in the vault.
// Paths that cannot be resolved count as missing (Python raised here, ending
// the whole run, when a model proposed moving a symlink that leaves the vault).
func exists(v *vault.Vault, rel string) bool {
	abs, err := v.Resolve(rel)
	if err != nil {
		return false
	}
	_, err = os.Stat(abs)
	return err == nil
}

// planOperations returns the plan's operations that are objects (Python:
// [op for op in plan.get("operations") or [] if isinstance(op, dict)]).
func planOperations(plan *pyyaml.Map) []*pyyaml.Map {
	list, _ := plan.Value("operations").([]any)
	ops := []*pyyaml.Map{}
	for _, item := range list {
		if op, ok := item.(*pyyaml.Map); ok && op != nil {
			ops = append(ops, op)
		}
	}
	return ops
}

// sourceMaterial is the text of the chats to distil, clipped to limit.
// Chats that cannot be read are left out.
func sourceMaterial(v *vault.Vault, chatPaths []string, limit int) string {
	blocks := []string{}
	for _, rel := range chatPaths {
		note, err := v.ReadNote(rel)
		if err != nil {
			logSkipped(rel, err)
			continue
		}
		blocks = append(blocks, "===== 对话："+rel+" =====\n"+note.Content)
	}
	return clip(strings.Join(blocks, "\n\n"), limit)
}

// get is dict.get(key, fallback).
func get(m *pyyaml.Map, key string, fallback any) any {
	if v, ok := m.Get(key); ok {
		return v
	}
	return fallback
}

// text converts a plan value to an event string: None becomes "" (Python
// sent null; the frontend treats both as empty), other values use str().
func text(v any) string {
	if v == nil {
		return ""
	}
	return pyyaml.Str(v)
}

var (
	fencedJSONRE = regexp.MustCompile("(?s)```(?:json)?[" + pystr.SpaceClass + "]*(\\{.*\\})[" + pystr.SpaceClass + "]*```")
	fencedNoteRE = regexp.MustCompile("\\A```(?:markdown|md)?[" + pystr.SpaceClass + "]*\n((?s:.*))\n```\\z")
)

// ExtractJSON parses the JSON object in a model's answer: the content of a
// ```json fence when there is one, else the text from the first "{" to the
// last "}". Values use the Python value model of package pyyaml, so the plan
// is interpreted exactly as Python's json.loads result was.
func ExtractJSON(answer string) (*pyyaml.Map, error) {
	s := pystr.Strip(answer)
	if m := fencedJSONRE.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < 0 {
		return nil, &Error{Message: "AI 没有返回有效的修改计划"}
	}
	raw := ""
	if end >= start {
		raw = s[start : end+1]
	}
	value, err := pyyaml.FromJSON([]byte(raw))
	if err != nil {
		return nil, &Error{Message: "无法解析 AI 的修改计划：" + err.Error(), Err: err}
	}
	plan, ok := value.(*pyyaml.Map)
	if !ok || plan == nil {
		return nil, &Error{Message: "无法解析 AI 的修改计划：不是 JSON 对象"}
	}
	return plan, nil
}

// StripFence returns a written note without the ```markdown (or ```md, or
// bare ```) fence some models wrap it in, trimmed and ending in exactly one
// newline.
func StripFence(answer string) string {
	s := pystr.Strip(answer)
	if m := fencedNoteRE.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	return pystr.Strip(s) + "\n"
}
