package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"marginalia/internal/vault"
	"marginalia/internal/vault/vaulttest"
)

type scriptedTurn struct {
	deltas []string
	res    TurnResult
	err    error
}

type scriptedStreamer struct {
	calls []struct {
		model string
		msgs  []ToolMessage
		tools []ToolDef
	}
	turns []scriptedTurn
}

func (s *scriptedStreamer) StreamTurn(_ context.Context, model string, msgs []ToolMessage, tools []ToolDef, onDelta func(string) error) (TurnResult, error) {
	idx := len(s.calls)
	s.calls = append(s.calls, struct {
		model string
		msgs  []ToolMessage
		tools []ToolDef
	}{model: model, msgs: append([]ToolMessage(nil), msgs...), tools: tools})
	if idx >= len(s.turns) {
		return TurnResult{}, nil
	}
	turn := s.turns[idx]
	if turn.err != nil {
		return TurnResult{}, turn.err
	}
	for _, d := range turn.deltas {
		if onDelta != nil {
			if err := onDelta(d); err != nil {
				return TurnResult{}, err
			}
		}
	}
	return turn.res, nil
}

func chatTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(vaulttest.Strings(t, map[string]string{
		"AGENTS.md":           "# 约定\n- 使用中文\n",
		"Knowledge/PPO.md":    "# PPO\n第一段：策略优化。\n\n第二段：裁剪目标函数。\n",
		"Knowledge/GRPO.md":   "# GRPO\n相关：[[PPO]]\n",
		"Knowledge/Dup.md":    "重复行\n中间\n重复行\n",
		"Chats/2026-01-01.md": "---\ntype: chat\nstatus: inbox\n---\n历史对话\n",
		"assets/diagram.png":  "PNGDATA",
	}))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRunChatReadOnlyLoopDoesNotModifyDisk(t *testing.T) {
	v := chatTestVault(t)
	llm := &scriptedStreamer{
		turns: []scriptedTurn{
			{
				deltas: []string{"我先查一下。"},
				res: TurnResult{
					Content: "我先查一下。",
					ToolCalls: []ToolCall{
						{ID: "c1", Type: "function", Function: ToolCallFunction{Name: "search_notes", Arguments: `{"query":"裁剪"}`}},
						{ID: "c2", Type: "function", Function: ToolCallFunction{Name: "read_note", Arguments: `{"path":"Knowledge/PPO.md"}`}},
						{ID: "c3", Type: "function", Function: ToolCallFunction{Name: "get_backlinks", Arguments: `{"path":"Knowledge/PPO.md"}`}},
					},
				},
			},
			{
				deltas: []string{"PPO 使用裁剪目标函数。"},
				res:    TurnResult{Content: "PPO 使用裁剪目标函数。"},
			},
		},
	}

	var events []ChatEvent
	err := RunChat(context.Background(), llm, v, 20000, ChatRequest{
		Model:   "m1",
		History: []Message{{Role: RoleUser, Content: "PPO 用了什么目标？"}},
		Scope:   ScopeVault,
	}, func(ev ChatEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("RunChat error: %v", err)
	}

	var text strings.Builder
	var statuses []string
	var proposals []*Proposal
	for _, ev := range events {
		text.WriteString(ev.Delta)
		if ev.Status != "" {
			statuses = append(statuses, ev.Status)
		}
		if ev.Proposal != nil {
			proposals = append(proposals, ev.Proposal)
		}
	}
	if text.String() != "我先查一下。\n\nPPO 使用裁剪目标函数。" {
		t.Errorf("text = %q", text.String())
	}
	if len(statuses) != 3 {
		t.Errorf("statuses = %v", statuses)
	}
	if len(proposals) != 0 {
		t.Errorf("unexpected proposals: %+v", proposals)
	}
	if len(llm.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(llm.calls))
	}
	secondMsgs := llm.calls[1].msgs
	var toolOutputs []string
	for _, m := range secondMsgs {
		if m.Role == RoleTool && m.Content != nil {
			toolOutputs = append(toolOutputs, *m.Content)
		}
	}
	if len(toolOutputs) != 3 || !strings.Contains(toolOutputs[0], "Knowledge/PPO.md") ||
		!strings.Contains(toolOutputs[1], "裁剪目标函数") || !strings.Contains(toolOutputs[2], "Knowledge/GRPO.md") {
		t.Errorf("toolOutputs = %q", toolOutputs)
	}
}

func TestRunChatChainedEditsAndStagedRead(t *testing.T) {
	v := chatTestVault(t)
	diskNote, err := v.ReadNote("Knowledge/PPO.md")
	if err != nil {
		t.Fatal(err)
	}

	llm := &scriptedStreamer{
		turns: []scriptedTurn{
			{
				res: TurnResult{
					ToolCalls: []ToolCall{
						{
							ID:   "c1",
							Type: "function",
							Function: ToolCallFunction{
								Name:      "edit_note",
								Arguments: `{"path":"Knowledge/PPO.md","old_text":"第一段：策略优化。","new_text":"第一段：近端策略优化。","reason":"补充全称"}`,
							},
						},
					},
				},
			},
			{
				res: TurnResult{
					ToolCalls: []ToolCall{
						{
							ID:   "c2",
							Type: "function",
							Function: ToolCallFunction{
								Name:      "read_note",
								Arguments: `{"path":"Knowledge/PPO.md"}`,
							},
						},
						{
							ID:   "c3",
							Type: "function",
							Function: ToolCallFunction{
								Name:      "edit_note",
								Arguments: `{"path":"Knowledge/PPO.md","old_text":"第二段：裁剪目标函数。","new_text":"第二段：clip 裁剪目标。","reason":"精简表述"}`,
							},
						},
						{
							ID:   "c4",
							Type: "function",
							Function: ToolCallFunction{
								Name:      "write_note",
								Arguments: `{"path":"Inbox/总结.md","content":"# 总结\n\n要点","reason":"记录摘要"}`,
							},
						},
					},
				},
			},
			{
				deltas: []string{"已生成修改建议。"},
				res:    TurnResult{Content: "已生成修改建议。"},
			},
		},
	}

	var events []ChatEvent
	err = RunChat(context.Background(), llm, v, 20000, ChatRequest{
		History: []Message{{Role: RoleUser, Content: "改 PPO 并新建总结"}},
		Scope:   ScopeNote,
		Path:    "Knowledge/PPO.md",
	}, func(ev ChatEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("RunChat error: %v", err)
	}

	thirdMsgs := llm.calls[2].msgs
	var stagedRead string
	for _, m := range thirdMsgs {
		if m.Role == RoleTool && m.ToolCallID == "c2" && m.Content != nil {
			stagedRead = *m.Content
		}
	}
	if !strings.Contains(stagedRead, "status: staged") || !strings.Contains(stagedRead, "近端策略优化") {
		t.Errorf("staged read_note = %q", stagedRead)
	}

	afterDisk, _ := v.ReadNote("Knowledge/PPO.md")
	if afterDisk.Content != diskNote.Content {
		t.Fatalf("disk note was modified before apply")
	}
	if _, err := v.ReadNote("Inbox/总结.md"); err == nil {
		t.Fatalf("new note written to disk before apply")
	}

	var proposals []*Proposal
	for _, ev := range events {
		if ev.Proposal != nil {
			proposals = append(proposals, ev.Proposal)
		}
	}
	if len(proposals) != 2 {
		t.Fatalf("proposals = %+v, want 2", proposals)
	}

	p0 := proposals[0]
	if p0.Op != OpUpdate || p0.Path != "Knowledge/PPO.md" || p0.Before != diskNote.Content ||
		p0.BaseETag == nil || *p0.BaseETag != diskNote.ETag ||
		p0.After != "# PPO\n第一段：近端策略优化。\n\n第二段：clip 裁剪目标。\n" ||
		p0.Reason != "补充全称；精简表述" {
		t.Errorf("first proposal = %+v", p0)
	}
	p1 := proposals[1]
	if p1.Op != OpCreate || p1.Path != "Inbox/总结.md" || p1.Before != "" || p1.BaseETag != nil ||
		p1.After != "# 总结\n\n要点\n" {
		t.Errorf("second proposal = %+v", p1)
	}
}

func TestRunChatErrorRecoveryAndProtection(t *testing.T) {
	v := chatTestVault(t)
	llm := &scriptedStreamer{
		turns: []scriptedTurn{
			{
				res: TurnResult{
					ToolCalls: []ToolCall{
						{ID: "c1", Type: "function", Function: ToolCallFunction{Name: "edit_note", Arguments: `{bad json`}},
						{ID: "c2", Type: "function", Function: ToolCallFunction{Name: "edit_note", Arguments: `{"path":"Knowledge/PPO.md","old_text":"不存在","new_text":"x"}`}},
						{ID: "c3", Type: "function", Function: ToolCallFunction{Name: "edit_note", Arguments: `{"path":"Knowledge/Dup.md","old_text":"重复行","new_text":"x"}`}},
						{ID: "c4", Type: "function", Function: ToolCallFunction{Name: "write_note", Arguments: `{"path":"Chats/2026-01-01.md","content":"hack"}`}},
						{ID: "c5", Type: "function", Function: ToolCallFunction{Name: "write_note", Arguments: `{"path":"AGENTS.md","content":"hack"}`}},
						{ID: "c6", Type: "function", Function: ToolCallFunction{Name: "delete_note", Arguments: `{"path":"Chats/2026-01-01.md"}`}},
					},
				},
			},
			{
				deltas: []string{"无法修改受保护文件或未匹配文本。"},
				res:    TurnResult{Content: "无法修改受保护文件或未匹配文本。"},
			},
		},
	}

	var events []ChatEvent
	err := RunChat(context.Background(), llm, v, 20000, ChatRequest{
		History: []Message{{Role: RoleUser, Content: "test errors"}},
		Scope:   ScopeVault,
	}, func(ev ChatEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("RunChat error: %v", err)
	}

	for _, ev := range events {
		if ev.Proposal != nil {
			t.Fatalf("unexpected proposal on failed/protected calls: %+v", ev.Proposal)
		}
	}

	gotErrs := map[string]string{}
	for _, m := range llm.calls[1].msgs {
		if m.Role == RoleTool && m.Content != nil {
			gotErrs[m.ToolCallID] = *m.Content
		}
	}
	for id, substr := range map[string]string{
		"c1": "工具调用失败：参数 JSON 无效",
		"c2": "未在笔记中找到 old_text",
		"c3": "匹配到 2 处",
		"c4": "禁止修改受保护文件",
		"c5": "禁止修改受保护文件",
		"c6": "禁止修改受保护文件",
	} {
		if !strings.Contains(gotErrs[id], substr) {
			t.Errorf("call %s = %q, want substring %q", id, gotErrs[id], substr)
		}
	}
}

func TestRunChatMoveAndDeleteValidation(t *testing.T) {
	v := chatTestVault(t)
	llm := &scriptedStreamer{
		turns: []scriptedTurn{
			{
				res: TurnResult{
					ToolCalls: []ToolCall{
						{ID: "m_ok", Type: "function", Function: ToolCallFunction{Name: "move_note", Arguments: `{"path":"Knowledge/GRPO.md","to":"Knowledge/RL/GRPO.md","reason":"归类"}`}},
						{ID: "m_missing", Type: "function", Function: ToolCallFunction{Name: "move_note", Arguments: `{"path":"NoSuch.md","to":"B.md"}`}},
						{ID: "m_conflict", Type: "function", Function: ToolCallFunction{Name: "move_note", Arguments: `{"path":"Knowledge/Dup.md","to":"Knowledge/PPO.md"}`}},
						{ID: "d_ok", Type: "function", Function: ToolCallFunction{Name: "delete_note", Arguments: `{"path":"Knowledge/Dup.md","reason":"清理"}`}},
						{ID: "d_moved", Type: "function", Function: ToolCallFunction{Name: "delete_note", Arguments: `{"path":"Knowledge/GRPO.md"}`}},
					},
				},
			},
			{
				deltas: []string{"完成。"},
				res:    TurnResult{Content: "完成。"},
			},
		},
	}

	var proposals []*Proposal
	err := RunChat(context.Background(), llm, v, 20000, ChatRequest{
		History: []Message{{Role: RoleUser, Content: "整理"}},
		Scope:   ScopeVault,
	}, func(ev ChatEvent) error {
		if ev.Proposal != nil {
			proposals = append(proposals, ev.Proposal)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RunChat error: %v", err)
	}
	if len(proposals) != 2 {
		t.Fatalf("proposals = %+v, want 2", proposals)
	}
	if proposals[0].Op != OpMove || proposals[0].Path != "Knowledge/GRPO.md" || proposals[0].To != "Knowledge/RL/GRPO.md" || proposals[0].BaseETag == nil {
		t.Errorf("move proposal = %+v", proposals[0])
	}
	if proposals[1].Op != OpDelete || proposals[1].Path != "Knowledge/Dup.md" || proposals[1].BaseETag == nil {
		t.Errorf("delete proposal = %+v", proposals[1])
	}

	toolMsgs := map[string]string{}
	for _, m := range llm.calls[1].msgs {
		if m.Role == RoleTool && m.Content != nil {
			toolMsgs[m.ToolCallID] = *m.Content
		}
	}
	if !strings.Contains(toolMsgs["m_missing"], "源路径不存在") {
		t.Errorf("m_missing = %q", toolMsgs["m_missing"])
	}
	if !strings.Contains(toolMsgs["m_conflict"], "目标路径已存在") {
		t.Errorf("m_conflict = %q", toolMsgs["m_conflict"])
	}
	if !strings.Contains(toolMsgs["d_moved"], "已有暂存操作") {
		t.Errorf("d_moved = %q", toolMsgs["d_moved"])
	}
}

func TestRunChatFallbackWhenToolsUnsupported(t *testing.T) {
	v := chatTestVault(t)
	llm := &scriptedStreamer{
		turns: []scriptedTurn{
			{err: &Error{Message: "bad request", StatusCode: http.StatusBadRequest}},
			{deltas: []string{"降级后的回答"}, res: TurnResult{Content: "降级后的回答"}},
		},
	}

	var events []ChatEvent
	err := RunChat(context.Background(), llm, v, 20000, ChatRequest{
		History: []Message{{Role: RoleUser, Content: "hello"}},
		Scope:   ScopeNote,
		Path:    "Knowledge/PPO.md",
	}, func(ev ChatEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("RunChat error: %v", err)
	}
	if len(llm.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(llm.calls))
	}
	if len(llm.calls[0].tools) == 0 || llm.calls[1].tools != nil {
		t.Errorf("tools calls[0]=%d calls[1]=%v", len(llm.calls[0].tools), llm.calls[1].tools)
	}
	if strings.Contains(*llm.calls[1].msgs[0].Content, "edit_note") {
		t.Errorf("fallback system prompt still contains ChatToolsPrompt")
	}
	if !reflect.DeepEqual(events, []ChatEvent{{Delta: "降级后的回答"}}) {
		t.Errorf("events = %+v", events)
	}
}

func TestChatToolDefsJSONValid(t *testing.T) {
	defs := chatToolDefs()
	if len(defs) != 9 {
		t.Fatalf("len(defs) = %d, want 9", len(defs))
	}
	b, err := json.Marshal(defs)
	if err != nil || !strings.Contains(string(b), "edit_note") {
		t.Fatalf("marshal defs: %v", err)
	}
}
