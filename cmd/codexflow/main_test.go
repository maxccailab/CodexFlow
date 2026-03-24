package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRolePromptFirstTurn(t *testing.T) {
	state := TaskRuntimeState{
		Goal:         "完成一个最小闭环",
		WorkspaceDir: "/tmp/work",
		TurnCount:    0,
		MaxTurns:     12,
		ActiveRole:   "设计者",
	}
	role := RoleConfig{
		Name:             "设计者",
		Description:      "负责设计和实现",
		Instructions:     "根据目标推进实现",
		Prompt:           "先完成第一轮实现",
		AllowedNextRoles: []string{"审核者", "测试者"},
	}
	cfg := &TaskConfig{TopRole: "审核者"}

	got := buildRolePrompt(state, cfg, role)
	if !containsText(got, "你正在一个单任务、多角色、串行执行的自动编排器中。") {
		t.Fatalf("提示词缺少固定前缀: %q", got)
	}
	if !containsText(got, "唯一允许结束流程的置顶角色: 审核者") {
		t.Fatalf("提示词缺少置顶角色约束: %q", got)
	}
	if !containsText(got, "允许交接到: 审核者、测试者") {
		t.Fatalf("提示词缺少允许角色: %q", got)
	}
	if !containsText(got, "本轮指令: 先完成第一轮实现") {
		t.Fatalf("提示词缺少首轮指令: %q", got)
	}
	if !containsText(got, "除非当前状态是明确 blocked，否则本轮必须产出至少一种可检查结果") {
		t.Fatalf("提示词缺少强制产出要求: %q", got)
	}
	if !containsText(got, "reply_to_user 用简洁中文说明本轮检查了什么、做了什么、结果是什么，并尽量写出具体文件、命令或产出物") {
		t.Fatalf("提示词缺少具体交接要求: %q", got)
	}
}

func TestBuildRolePromptUsesLastHandoffPrompt(t *testing.T) {
	state := TaskRuntimeState{
		Goal:         "完成一个最小闭环",
		WorkspaceDir: "/tmp/work",
		TurnCount:    1,
		MaxTurns:     12,
		ActiveRole:   "审核者",
		LastHandoff: &Handoff{
			HandoffSummary: "已完成初版",
			HandoffItems:   []string{"检查边界条件", "补充验证"},
		},
	}
	role := RoleConfig{
		Name:             "审核者",
		Description:      "负责审核结果",
		Instructions:     "检查实现是否满足目标",
		Prompt:           "审查最新结果",
		AllowedNextRoles: []string{"设计者"},
	}
	cfg := &TaskConfig{TopRole: "审核者"}

	got := buildRolePrompt(state, cfg, role)
	if !containsText(got, "最近一次交接摘要: 已完成初版") {
		t.Fatalf("提示词缺少交接摘要: %q", got)
	}
	if !containsText(got, "最近一次交接事项:\n- 检查边界条件\n- 补充验证") {
		t.Fatalf("提示词缺少交接事项: %q", got)
	}
	if !containsText(got, "本轮指令: 审查最新结果") {
		t.Fatalf("提示词未使用角色默认 prompt: %q", got)
	}
}

func TestValidateConfig(t *testing.T) {
	cfg := &TaskConfig{
		TaskID:      "todo-api",
		Goal:        "完成一个最小闭环",
		InitialRole: "设计者",
		TopRole:     "审核者",
		MaxTurns:    12,
		Roles: []RoleConfig{
			{Name: "设计者", AllowedNextRoles: []string{"审核者", "测试者"}},
			{Name: "审核者", AllowedNextRoles: []string{"设计者"}},
			{Name: "测试者", AllowedNextRoles: []string{"设计者"}},
		},
	}

	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestValidateHandoffContinue(t *testing.T) {
	cfg := &TaskConfig{TopRole: "审核者"}
	role := RoleConfig{Name: "设计者", AllowedNextRoles: []string{"审核者", "测试者"}}
	handoff := &Handoff{
		Status:               "continue",
		ReplyToUser:          "已完成实现并建议交给测试者",
		HandoffSummary:       "当前实现可进入回归检查",
		NextRole:             "测试者",
		CompletionConfidence: 0.8,
	}

	if err := validateHandoff(cfg, role, handoff); err != nil {
		t.Fatalf("validateHandoff() error = %v", err)
	}
}

func TestFormatRoleMessageIncludesCompletionReason(t *testing.T) {
	handoff := &Handoff{
		Status:           "blocked",
		ReplyToUser:      "已定位阻塞点",
		CompletionReason: "缺少必要配置文件",
	}

	got := formatRoleMessage(handoff)
	if !strings.Contains(got, "已定位阻塞点") || !strings.Contains(got, "结束原因: 缺少必要配置文件") {
		t.Fatalf("unexpected formatted message: %q", got)
	}
}

func TestFormatRoleMessageIncludesNextStep(t *testing.T) {
	handoff := &Handoff{
		Status:      "continue",
		ReplyToUser: "本轮已完成实现",
		NextRole:    "测试者",
	}

	got := formatRoleMessage(handoff)
	if !strings.Contains(got, "本轮已完成实现") ||
		!strings.Contains(got, "下一角色: 测试者") {
		t.Fatalf("unexpected formatted message: %q", got)
	}
}

func TestValidateHandoffRejectsEmptyUserReply(t *testing.T) {
	cfg := &TaskConfig{TopRole: "审核者"}
	role := RoleConfig{Name: "设计者", AllowedNextRoles: []string{"审核者"}}
	handoff := &Handoff{
		Status:               "continue",
		HandoffSummary:       "准备交给审核者",
		NextRole:             "审核者",
		CompletionConfidence: 0.8,
	}

	err := validateHandoff(cfg, role, handoff)
	if err == nil || !strings.Contains(err.Error(), "reply_to_user") {
		t.Fatalf("expected reply_to_user validation error, got %v", err)
	}
}

func TestValidateHandoffRejectsEmptySummary(t *testing.T) {
	cfg := &TaskConfig{TopRole: "审核者"}
	role := RoleConfig{Name: "设计者", AllowedNextRoles: []string{"审核者"}}
	handoff := &Handoff{
		Status:               "continue",
		ReplyToUser:          "已完成实现并交接",
		NextRole:             "审核者",
		CompletionConfidence: 0.8,
	}

	err := validateHandoff(cfg, role, handoff)
	if err == nil || !strings.Contains(err.Error(), "handoff_summary") {
		t.Fatalf("expected handoff_summary validation error, got %v", err)
	}
}

func TestValidateConfigRequiresTopRole(t *testing.T) {
	cfg := &TaskConfig{
		TaskID:      "todo-api",
		Goal:        "完成一个最小闭环",
		InitialRole: "设计者",
		MaxTurns:    12,
		Roles: []RoleConfig{
			{Name: "设计者", AllowedNextRoles: []string{"审核者"}},
			{Name: "审核者", AllowedNextRoles: []string{"设计者"}},
		},
	}

	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "top_role") {
		t.Fatalf("expected top_role validation error, got %v", err)
	}
}

func TestValidateHandoffRejectsCompleteFromNonTopRole(t *testing.T) {
	cfg := &TaskConfig{TopRole: "审核者"}
	role := RoleConfig{Name: "设计者", AllowedNextRoles: []string{"审核者"}}
	handoff := &Handoff{
		Status:               "complete",
		ReplyToUser:          "主体工作已完成，等待最终判定",
		HandoffSummary:       "当前实现已完成，但应继续流转给置顶角色",
		CompletionReason:     "主体工作完成",
		CompletionConfidence: 0.9,
	}

	err := validateHandoff(cfg, role, handoff)
	if err == nil || !strings.Contains(err.Error(), "不能返回 complete") {
		t.Fatalf("expected complete restriction error, got %v", err)
	}
}

func TestValidateHandoffAllowsCompleteFromTopRole(t *testing.T) {
	cfg := &TaskConfig{TopRole: "审核者"}
	role := RoleConfig{Name: "审核者", AllowedNextRoles: []string{"设计者"}}
	handoff := &Handoff{
		Status:               "complete",
		ReplyToUser:          "已确认任务满足目标，可结束流程",
		HandoffSummary:       "置顶角色完成最终验收",
		CompletionReason:     "验收通过",
		CompletionConfidence: 0.95,
	}

	if err := validateHandoff(cfg, role, handoff); err != nil {
		t.Fatalf("validateHandoff() error = %v", err)
	}
}

func TestSaveAndLoadRuntimeState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, runtimeStateFileName)
	state := TaskRuntimeState{
		Goal:         "完成一个最小闭环",
		WorkspaceDir: dir,
		TurnCount:    2,
		MaxTurns:     12,
		ActiveRole:   "审核者",
		LastHandoff: &Handoff{
			FromRole:             "设计者",
			Status:               "continue",
			ReplyToUser:          "已完成第一轮实现",
			HandoffSummary:       "交给审核者继续检查",
			HandoffItems:         []string{"检查边界条件"},
			NextRole:             "审核者",
			CompletionReason:     "",
			CompletionConfidence: 0.9,
		},
	}

	if err := saveRuntimeState(path, state); err != nil {
		t.Fatalf("saveRuntimeState() error = %v", err)
	}

	loaded, err := loadRuntimeState(path)
	if err != nil {
		t.Fatalf("loadRuntimeState() error = %v", err)
	}

	if loaded.ActiveRole != state.ActiveRole || loaded.TurnCount != state.TurnCount {
		t.Fatalf("loaded state mismatch: %+v", loaded)
	}
	if loaded.LastHandoff == nil || loaded.LastHandoff.HandoffSummary != state.LastHandoff.HandoffSummary {
		t.Fatalf("loaded handoff mismatch: %+v", loaded.LastHandoff)
	}
}

func TestRolePaths(t *testing.T) {
	sessionFile, outputFile, rawOutputFile := rolePaths("/tmp/state", "设计者")
	if sessionFile != "/tmp/state/设计者.session" {
		t.Fatalf("unexpected session file: %s", sessionFile)
	}
	if outputFile != "/tmp/state/设计者.last-output.json" {
		t.Fatalf("unexpected output file: %s", outputFile)
	}
	if rawOutputFile != "/tmp/state/设计者.last-raw.txt" {
		t.Fatalf("unexpected raw output file: %s", rawOutputFile)
	}
}

func TestAppendHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), historyFileName)
	entry := HistoryEntry{
		Turn:             1,
		Role:             "设计者",
		Status:           "continue",
		NextRole:         "审核者",
		ReplyToUser:      "已完成首轮实现",
		HandoffSummary:   "交给审核者检查",
		HandoffItems:     []string{"检查边界条件"},
		CompletionReason: "",
		OutputFile:       "/tmp/out.json",
		RawOutputFile:    "/tmp/raw.txt",
	}

	if err := appendHistory(path, entry); err != nil {
		t.Fatalf("appendHistory() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("unexpected history line count: %d", len(lines))
	}

	var got HistoryEntry
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.Role != entry.Role || got.NextRole != entry.NextRole || got.OutputFile != entry.OutputFile {
		t.Fatalf("unexpected history entry: %+v", got)
	}
}

func TestMultiWriterWritesToTerminalAndBuffer(t *testing.T) {
	var terminal bytes.Buffer
	var captured bytes.Buffer

	writer := io.MultiWriter(&terminal, &captured)
	if _, err := writer.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if terminal.String() != "hello" {
		t.Fatalf("unexpected terminal output: %q", terminal.String())
	}
	if captured.String() != "hello" {
		t.Fatalf("unexpected captured output: %q", captured.String())
	}
}

func TestLoadRuntimeStateNotExist(t *testing.T) {
	_, err := loadRuntimeState(filepath.Join(t.TempDir(), "missing.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("expected not exist error, got %v", err)
	}
}

func TestShouldResetSession(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "invalid session", output: "Error: invalid session id", want: true},
		{name: "session not found", output: "session not found", want: true},
		{name: "no session", output: "no session is available", want: true},
		{name: "other error", output: "network timeout", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldResetSession(tc.output); got != tc.want {
				t.Fatalf("shouldResetSession(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

func TestParseSessionID(t *testing.T) {
	output := "foo\nsession id: 123e4567-e89b-12d3-a456-426614174000\nbar"
	got := parseSessionID(output)
	want := "123e4567-e89b-12d3-a456-426614174000"
	if got != want {
		t.Fatalf("parseSessionID() = %q, want %q", got, want)
	}
}

func TestBuildCodexArgsForNewSession(t *testing.T) {
	got := buildCodexArgs("/tmp/work", "执行任务", "/tmp/out.json", "", "/tmp/schema.json")
	want := []string{
		"exec",
		"--color", "never",
		"--dangerously-bypass-approvals-and-sandbox",
		"--cd", "/tmp/work",
		"--output-schema", "/tmp/schema.json",
		"-o", "/tmp/out.json",
		"执行任务",
	}

	if len(got) != len(want) {
		t.Fatalf("buildCodexArgs() length = %d, want %d, got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("buildCodexArgs()[%d] = %q, want %q; full=%v", i, got[i], want[i], got)
		}
	}
}

func TestBuildCodexArgsForResumeSession(t *testing.T) {
	got := buildCodexArgs("/tmp/work", "继续执行", "/tmp/out.json", "session-123", "/tmp/schema.json")
	want := []string{
		"exec",
		"--color", "never",
		"--dangerously-bypass-approvals-and-sandbox",
		"--cd", "/tmp/work",
		"--output-schema", "/tmp/schema.json",
		"-o", "/tmp/out.json",
		"resume", "session-123",
		"继续执行",
	}

	if len(got) != len(want) {
		t.Fatalf("buildCodexArgs() length = %d, want %d, got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("buildCodexArgs()[%d] = %q, want %q; full=%v", i, got[i], want[i], got)
		}
	}
}

func containsText(text string, want string) bool {
	return strings.Contains(text, want)
}
