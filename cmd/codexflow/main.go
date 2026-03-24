package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	stateDirName           = ".codexflow"
	roleOutputSchemaName   = "role-output.schema.json"
	roleOutputJSONFileName = "last-output.json"
	roleRawOutputFileName  = "last-raw.txt"
	historyFileName        = "history.jsonl"
	runtimeStateFileName   = "runtime-state.json"
)

var sessionIDPattern = regexp.MustCompile(`session id:\s*([0-9a-fA-F-]+)`)

type TaskConfig struct {
	TaskID      string       `json:"task_id"`
	Goal        string       `json:"goal"`
	InitialRole string       `json:"initial_role"`
	TopRole     string       `json:"top_role"`
	MaxTurns    int          `json:"max_turns"`
	Roles       []RoleConfig `json:"roles"`
}

type RoleConfig struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Instructions     string   `json:"instructions"`
	Prompt           string   `json:"prompt"`
	AllowedNextRoles []string `json:"allowed_next_roles"`
}

type Handoff struct {
	FromRole             string   `json:"from_role"`
	Status               string   `json:"status"`
	ReplyToUser          string   `json:"reply_to_user"`
	HandoffSummary       string   `json:"handoff_summary"`
	HandoffItems         []string `json:"handoff_items"`
	NextRole             string   `json:"next_role"`
	CompletionReason     string   `json:"completion_reason"`
	CompletionConfidence float64  `json:"completion_confidence"`
}

type TaskRuntimeState struct {
	Goal         string   `json:"goal"`
	WorkspaceDir string   `json:"workspace_dir"`
	TurnCount    int      `json:"turn_count"`
	MaxTurns     int      `json:"max_turns"`
	ActiveRole   string   `json:"active_role"`
	LastHandoff  *Handoff `json:"last_handoff,omitempty"`
}

type RoleRunResult struct {
	Handoff       *Handoff
	OutputFile    string
	RawOutputFile string
}

type HistoryEntry struct {
	Turn             int      `json:"turn"`
	Role             string   `json:"role"`
	Status           string   `json:"status"`
	NextRole         string   `json:"next_role"`
	ReplyToUser      string   `json:"reply_to_user"`
	HandoffSummary   string   `json:"handoff_summary"`
	HandoffItems     []string `json:"handoff_items"`
	CompletionReason string   `json:"completion_reason"`
	OutputFile       string   `json:"output_file"`
	RawOutputFile    string   `json:"raw_output_file"`
}

func main() {
	configPath := flag.String("config", "", "任务配置 JSON 文件路径")
	dir := flag.String("dir", ".", "工作目录，相对路径相对于当前启动目录解析")
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "codexflow: --config 是必填参数")
		os.Exit(1)
	}

	if err := run(*configPath, *dir); err != nil {
		fmt.Fprintf(os.Stderr, "codexflow: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string, dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("解析目录 %q 失败: %w", dir, err)
	}

	info, err := os.Stat(absDir)
	if err != nil {
		return fmt.Errorf("无法访问目录 %q: %w", absDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q 不是目录", absDir)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}

	stateDir := filepath.Join(absDir, stateDirName, cfg.TaskID)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("创建状态目录失败: %w", err)
	}

	codexPath, err := exec.LookPath("codex")
	if err != nil {
		return fmt.Errorf("无法在 PATH 中找到 codex: %w", err)
	}

	schemaPath := filepath.Join(stateDir, roleOutputSchemaName)
	if err := os.WriteFile(schemaPath, []byte(roleOutputSchema), 0o644); err != nil {
		return fmt.Errorf("写入输出 schema 失败: %w", err)
	}

	state := TaskRuntimeState{
		Goal:         cfg.Goal,
		WorkspaceDir: absDir,
		TurnCount:    0,
		MaxTurns:     cfg.MaxTurns,
		ActiveRole:   cfg.InitialRole,
	}
	runtimeStatePath := filepath.Join(stateDir, runtimeStateFileName)
	historyPath := filepath.Join(stateDir, historyFileName)
	if loadedState, err := loadRuntimeState(runtimeStatePath); err == nil {
		loadedState.Goal = cfg.Goal
		loadedState.WorkspaceDir = absDir
		loadedState.MaxTurns = cfg.MaxTurns
		if strings.TrimSpace(loadedState.ActiveRole) == "" {
			loadedState.ActiveRole = cfg.InitialRole
		}
		state = *loadedState
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取运行状态失败: %w", err)
	}
	if err := saveRuntimeState(runtimeStatePath, state); err != nil {
		return err
	}

	for state.TurnCount < state.MaxTurns {
		role := cfg.roleByName(state.ActiveRole)
		if role == nil {
			return fmt.Errorf("未找到角色 %q", state.ActiveRole)
		}

		prompt := buildRolePrompt(state, cfg, *role)
		result, err := runRole(codexPath, absDir, stateDir, *role, prompt, schemaPath)
		if err != nil {
			return err
		}
		handoff := result.Handoff

		fmt.Printf("\n[第 %d 轮][%s]\n%s\n", state.TurnCount+1, role.Name, formatRoleMessage(handoff))

		if err := validateHandoff(cfg, *role, handoff); err != nil {
			return fmt.Errorf("角色 %q 输出校验失败: %w；结构化输出文件: %s；原始输出文件: %s", role.Name, err, result.OutputFile, result.RawOutputFile)
		}

		if err := appendHistory(historyPath, HistoryEntry{
			Turn:             state.TurnCount + 1,
			Role:             role.Name,
			Status:           handoff.Status,
			NextRole:         handoff.NextRole,
			ReplyToUser:      handoff.ReplyToUser,
			HandoffSummary:   handoff.HandoffSummary,
			HandoffItems:     handoff.HandoffItems,
			CompletionReason: handoff.CompletionReason,
			OutputFile:       result.OutputFile,
			RawOutputFile:    result.RawOutputFile,
		}); err != nil {
			return err
		}

		state.TurnCount++
		handoff.FromRole = role.Name
		state.LastHandoff = handoff

		switch handoff.Status {
		case "complete", "blocked":
			if err := saveRuntimeState(runtimeStatePath, state); err != nil {
				return err
			}
			return nil
		case "continue":
			state.ActiveRole = handoff.NextRole
			if err := saveRuntimeState(runtimeStatePath, state); err != nil {
				return err
			}
		default:
			return fmt.Errorf("角色 %q 返回了未知状态 %q", role.Name, handoff.Status)
		}
	}

	if err := saveRuntimeState(runtimeStatePath, state); err != nil {
		return err
	}
	fmt.Printf("\n流程结束：已达到最大轮次 %d。\n", state.MaxTurns)
	return nil
}

func loadConfig(configPath string) (*TaskConfig, error) {
	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("解析配置路径 %q 失败: %w", configPath, err)
	}

	data, err := os.ReadFile(absConfigPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %q 失败: %w", absConfigPath, err)
	}

	var cfg TaskConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %q 失败: %w", absConfigPath, err)
	}
	return &cfg, nil
}

func validateConfig(cfg *TaskConfig) error {
	if strings.TrimSpace(cfg.TaskID) == "" {
		return fmt.Errorf("配置中的 task_id 不能为空")
	}
	if strings.TrimSpace(cfg.Goal) == "" {
		return fmt.Errorf("配置中的 goal 不能为空")
	}
	if strings.TrimSpace(cfg.InitialRole) == "" {
		return fmt.Errorf("配置中的 initial_role 不能为空")
	}
	if strings.TrimSpace(cfg.TopRole) == "" {
		return fmt.Errorf("配置中的 top_role 不能为空")
	}
	if cfg.MaxTurns <= 0 {
		return fmt.Errorf("配置中的 max_turns 必须大于 0")
	}
	if len(cfg.Roles) == 0 {
		return fmt.Errorf("配置中的 roles 不能为空")
	}

	seen := make(map[string]struct{}, len(cfg.Roles))
	for _, role := range cfg.Roles {
		if strings.TrimSpace(role.Name) == "" {
			return fmt.Errorf("角色 name 不能为空")
		}
		if _, ok := seen[role.Name]; ok {
			return fmt.Errorf("角色 %q 重复定义", role.Name)
		}
		seen[role.Name] = struct{}{}
	}

	if cfg.roleByName(cfg.InitialRole) == nil {
		return fmt.Errorf("initial_role %q 未在 roles 中定义", cfg.InitialRole)
	}
	if cfg.roleByName(cfg.TopRole) == nil {
		return fmt.Errorf("top_role %q 未在 roles 中定义", cfg.TopRole)
	}

	for _, role := range cfg.Roles {
		for _, next := range role.AllowedNextRoles {
			if cfg.roleByName(next) == nil {
				return fmt.Errorf("角色 %q 引用了未定义的下一角色 %q", role.Name, next)
			}
		}
	}

	return nil
}

func (cfg *TaskConfig) roleByName(name string) *RoleConfig {
	for i := range cfg.Roles {
		if cfg.Roles[i].Name == name {
			return &cfg.Roles[i]
		}
	}
	return nil
}

func buildRolePrompt(state TaskRuntimeState, cfg *TaskConfig, role RoleConfig) string {
	lastHandoffSummary := "无"
	lastHandoffItems := "无"

	if state.LastHandoff != nil {
		lastHandoffSummary = emptyFallback(state.LastHandoff.HandoffSummary, "无")
		if len(state.LastHandoff.HandoffItems) > 0 {
			lastHandoffItems = "- " + strings.Join(state.LastHandoff.HandoffItems, "\n- ")
		}
	}

	statusRules := []string{
		"状态判定:",
		"1. 只有在存在明确阻塞且当前无法继续推进时，才返回 blocked。",
		"2. 否则返回 continue，并从允许交接列表中选择 next_role。",
	}
	if role.Name == cfg.TopRole {
		statusRules = []string{
			"状态判定:",
			"1. 只有在确认任务目标已经达成且没有明确待办时，才返回 complete。",
			"2. 如果存在明确阻塞且当前无法继续推进，可以返回 blocked。",
			"3. 否则返回 continue，并从允许交接列表中选择 next_role。",
		}
	}
	outputRules := []string{
		"输出要求:",
		"1. 最终输出必须严格符合给定 JSON Schema，不要输出额外文本。",
		"2. reply_to_user 必须使用简洁中文，基于你本轮实际执行的检查、命令、修改或验证来说明：你检查了什么、做了什么、结果是什么。",
		"3. 如果本轮修改了文件、更新了文档、补充了测试、执行了命令，或得到关键检查结论，必须在 reply_to_user 中写明具体文件、命令或产出。",
		"4. handoff_summary 必须用一句话概括本轮最重要的实际结果，不能只写空泛结论。",
		"5. handoff_items 必须只列出下一步真正需要关注的具体事项，优先写明要查看的文件、待补的验证、待继续的修改或待确认的阻塞；没有则返回空数组。",
		"6. 如果 status=continue，next_role 必须从允许交接列表中选择。",
		"7. 如果 status=blocked，completion_reason 和 reply_to_user 必须明确说明阻塞点、阻塞原因，以及当前为什么无法继续推进。",
		"8. completion_confidence 是 0 到 1 的数字，表示你对当前判断的把握。",
	}
	if role.Name == cfg.TopRole {
		outputRules = append(outputRules,
			"9. status 只能是 continue、blocked、complete。",
			"10. 如果 status=complete 或 blocked，next_role 必须为空字符串。",
		)
	} else {
		outputRules = append(outputRules,
			"9. status 只能是 continue、blocked。",
			"10. 如果 status=blocked，next_role 必须为空字符串。",
		)
	}

	sections := []string{
		"你正在一个单任务、多角色、串行执行的自动编排器中。",
		strings.Join([]string{
			"任务上下文:",
			fmt.Sprintf("- 任务目标: %s", state.Goal),
			fmt.Sprintf("- 工作目录: %s", state.WorkspaceDir),
			fmt.Sprintf("- 当前总轮次: %d / %d", state.TurnCount+1, state.MaxTurns),
			fmt.Sprintf("- 当前角色: %s", role.Name),
			fmt.Sprintf("- 角色描述: %s", emptyFallback(role.Description, "无")),
			fmt.Sprintf("- 角色职责: %s", emptyFallback(role.Instructions, "无")),
			fmt.Sprintf("- 允许交接到: %s", allowedRolesText(role.AllowedNextRoles)),
			fmt.Sprintf("- 最近一次交接摘要: %s", lastHandoffSummary),
			fmt.Sprintf("- 最近一次交接事项:\n%s", lastHandoffItems),
			fmt.Sprintf("- 本轮指令: %s", emptyFallback(role.Prompt, "无")),
		}, "\n"),
		strings.Join([]string{
			"本轮要求:",
			"1. 由于你是独立 session，开始行动前必须先基于任务目标、最近交接信息和相关文件/产出做最小必要核查。",
			"2. 不要直接信任未核实的交接结论；如与实际代码、文件或命令结果不一致，以本轮核查结果为准。",
			"3. 优先执行当前最能推进目标的一步，把范围控制在本轮最小闭环内。",
			"4. 不要编造未执行过的检查、命令、修改、验证或结论。",
			"5. 除非当前状态是明确 blocked，否则本轮必须产出至少一种可检查结果，例如代码修改、文档更新、测试补充、命令执行记录或检查结论，不能只停留在空泛表述。",
		}, "\n"),
		strings.Join(statusRules, "\n"),
		strings.Join(outputRules, "\n"),
	}
	return strings.Join(sections, "\n\n")
}

func emptyFallback(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func allowedRolesText(roles []string) string {
	if len(roles) == 0 {
		return "无"
	}
	return strings.Join(roles, "、")
}

func formatRoleMessage(handoff *Handoff) string {
	message := emptyFallback(handoff.ReplyToUser, "无")
	if handoff.Status == "continue" {
		parts := []string{message}
		if strings.TrimSpace(handoff.NextRole) != "" {
			parts = append(parts, "下一角色: "+handoff.NextRole)
		}
		return strings.Join(parts, "\n")
	}
	if (handoff.Status == "blocked" || handoff.Status == "complete") && strings.TrimSpace(handoff.CompletionReason) != "" {
		return message + "\n结束原因: " + handoff.CompletionReason
	}
	return message
}

func runRole(codexPath string, dir string, stateDir string, role RoleConfig, prompt string, schemaPath string) (*RoleRunResult, error) {
	sessionFile, outputFile, rawOutputFile := rolePaths(stateDir, role.Name)
	if err := os.Remove(outputFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("重置角色 %s 输出失败: %w", role.Name, err)
	}
	if err := os.Remove(rawOutputFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("重置角色 %s 原始输出失败: %w", role.Name, err)
	}

	rawOutput, err := runCodexCommand(codexPath, dir, prompt, outputFile, sessionFile, schemaPath)
	if writeErr := os.WriteFile(rawOutputFile, []byte(rawOutput), 0o644); writeErr != nil {
		return nil, fmt.Errorf("写入角色 %s 原始输出失败: %w", role.Name, writeErr)
	}
	if err != nil {
		return nil, fmt.Errorf("角色 %s 执行失败，原始输出文件: %s: %w", role.Name, rawOutputFile, err)
	}

	data, err := os.ReadFile(outputFile)
	if err != nil {
		return nil, fmt.Errorf("读取角色 %s 输出失败: %w；结构化输出文件: %s；原始输出文件: %s", role.Name, err, outputFile, rawOutputFile)
	}

	var handoff Handoff
	if err := json.Unmarshal(data, &handoff); err != nil {
		return nil, fmt.Errorf("解析角色 %s 输出 JSON 失败: %w；结构化输出文件: %s；原始输出文件: %s", role.Name, err, outputFile, rawOutputFile)
	}
	return &RoleRunResult{
		Handoff:       &handoff,
		OutputFile:    outputFile,
		RawOutputFile: rawOutputFile,
	}, nil
}

func loadRuntimeState(path string) (*TaskRuntimeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state TaskRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析运行状态失败: %w", err)
	}
	return &state, nil
}

func saveRuntimeState(path string, state TaskRuntimeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化运行状态失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("写入运行状态失败: %w", err)
	}
	return nil
}

func appendHistory(path string, entry HistoryEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("序列化历史记录失败: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开历史记录文件失败: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("写入历史记录失败: %w", err)
	}
	return nil
}

func rolePaths(stateDir string, role string) (string, string, string) {
	return filepath.Join(stateDir, role+".session"),
		filepath.Join(stateDir, role+"."+roleOutputJSONFileName),
		filepath.Join(stateDir, role+"."+roleRawOutputFileName)
}

func runCodexCommand(codexPath string, dir string, prompt string, outputFile string, sessionFile string, schemaPath string) (string, error) {
	sessionID, err := readSessionID(sessionFile)
	if err == nil && sessionID != "" {
		output, runErr := executeCodex(codexPath, dir, prompt, outputFile, sessionID, schemaPath)
		if runErr == nil {
			if err := storeSessionID(sessionFile, output); err != nil {
				return "", err
			}
			return output, nil
		}
		if !shouldResetSession(output) {
			return "", fmt.Errorf("恢复会话失败: %w", runErr)
		}
		if err := os.Remove(sessionFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("重置失效会话文件失败: %w", err)
		}
	}

	output, err := executeCodex(codexPath, dir, prompt, outputFile, "", schemaPath)
	if err != nil {
		return "", fmt.Errorf("启动新会话失败: %w", err)
	}
	if err := storeSessionID(sessionFile, output); err != nil {
		return "", err
	}
	return output, nil
}

func executeCodex(codexPath string, dir string, prompt string, outputFile string, sessionID string, schemaPath string) (string, error) {
	args := buildCodexArgs(dir, prompt, outputFile, sessionID, schemaPath)

	cmd := exec.Command(codexPath, args...)
	cmd.Dir = dir

	var combined bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &combined)
	cmd.Stderr = io.MultiWriter(os.Stderr, &combined)

	err := cmd.Run()
	outputText := combined.String()
	if err != nil {
		return outputText, err
	}
	return outputText, nil
}

func buildCodexArgs(dir string, prompt string, outputFile string, sessionID string, schemaPath string) []string {
	args := []string{
		"exec",
		"--color", "never",
		"--dangerously-bypass-approvals-and-sandbox",
		"--cd", dir,
		"--output-schema", schemaPath,
		"-o", outputFile,
	}

	if sessionID != "" {
		args = append(args, "resume", sessionID)
	}

	args = append(args, prompt)
	return args
}

func readSessionID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func parseSessionID(output string) string {
	match := sessionIDPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func storeSessionID(sessionFile string, output string) error {
	sessionID := parseSessionID(output)
	if sessionID == "" {
		return nil
	}
	if err := os.WriteFile(sessionFile, []byte(sessionID), 0o644); err != nil {
		return fmt.Errorf("保存会话 ID 失败: %w", err)
	}
	return nil
}

func shouldResetSession(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "session not found") ||
		strings.Contains(lower, "invalid session") ||
		strings.Contains(lower, "could not find session") ||
		strings.Contains(lower, "no session")
}

func validateHandoff(cfg *TaskConfig, role RoleConfig, handoff *Handoff) error {
	switch handoff.Status {
	case "continue", "blocked", "complete":
	default:
		return fmt.Errorf("角色 %q 返回的 status 非法: %q", role.Name, handoff.Status)
	}

	if handoff.CompletionConfidence < 0 || handoff.CompletionConfidence > 1 {
		return fmt.Errorf("角色 %q 返回的 completion_confidence 超出范围", role.Name)
	}
	if strings.TrimSpace(handoff.ReplyToUser) == "" {
		return fmt.Errorf("角色 %q 返回的 reply_to_user 不能为空", role.Name)
	}
	if strings.TrimSpace(handoff.HandoffSummary) == "" {
		return fmt.Errorf("角色 %q 返回的 handoff_summary 不能为空", role.Name)
	}

	if handoff.Status == "continue" {
		if strings.TrimSpace(handoff.NextRole) == "" {
			return fmt.Errorf("角色 %q 选择 continue 时 next_role 不能为空", role.Name)
		}
		if !contains(role.AllowedNextRoles, handoff.NextRole) {
			return fmt.Errorf("角色 %q 选择了未授权的下一角色 %q", role.Name, handoff.NextRole)
		}
		return nil
	}

	if strings.TrimSpace(handoff.NextRole) != "" {
		return fmt.Errorf("角色 %q 在非 continue 状态下 next_role 必须为空", role.Name)
	}
	if handoff.Status == "complete" && role.Name != cfg.TopRole {
		return fmt.Errorf("角色 %q 不是置顶角色 %q，不能返回 complete", role.Name, cfg.TopRole)
	}
	if handoff.Status == "blocked" && strings.TrimSpace(handoff.CompletionReason) == "" {
		return fmt.Errorf("角色 %q 返回 blocked 时 completion_reason 不能为空", role.Name)
	}
	return nil
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

const roleOutputSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "status": {
      "type": "string",
      "enum": ["continue", "blocked", "complete"]
    },
    "reply_to_user": {
      "type": "string"
    },
    "handoff_summary": {
      "type": "string"
    },
    "handoff_items": {
      "type": "array",
      "items": { "type": "string" }
    },
    "next_role": {
      "type": "string"
    },
    "completion_reason": {
      "type": "string"
    },
    "completion_confidence": {
      "type": "number"
    }
  },
  "required": [
    "status",
    "reply_to_user",
    "handoff_summary",
    "handoff_items",
    "next_role",
    "completion_reason",
    "completion_confidence"
  ]
}`
