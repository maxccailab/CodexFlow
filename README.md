# CodexFlow

一个按 Go 常规方式初始化的最小项目，并增加了通过 Go 调用本地 `codex` 命令行程序的示例。

## 结构

```text
.
├── cmd/
│   └── codexflow/
│       └── main.go
└── go.mod
```

## 运行

```bash
go run ./cmd/codexflow
```

## 测试

测试工作目录是 [testdir](/Users/cc/opensource/CodexFlow/testdir)。它主要是约定的测试启动目录，用来隔离执行入口，不是源码目录。

推荐从这个目录内启动测试，例如：

```bash
cd ./testdir
go test ../...
```

默认会调用本地 `codex exec`，并自动带上：

```bash
--dangerously-bypass-approvals-and-sandbox
--cd <dir>
```

其中 `--dir` 会作为 Codex 的工作目录。相对路径会相对于你启动 `go run` 时的当前目录解析。
`--config` 支持相对路径和绝对路径。

程序现在按 JSON 配置驱动角色流转。每个角色的 `session id` 都会保存在 `<dir>/.codexflow/<task_id>/` 目录下，后续再次运行时会自动继续对应会话。
如果保存的 session 已失效，程序会自动丢弃旧的 session 文件并重新开启新会话。
编排运行状态也会保存在 `<dir>/.codexflow/<task_id>/runtime-state.json`，重启后会从上次轮次和角色继续。
如果当前角色允许交接到多个角色，则由大模型在这些候选角色中选择 `next_role`。
每个角色最近一次的结构化输出和原始 CLI 输出也会保存在状态目录里，便于定位解析失败和校验失败的问题。
每一轮的摘要还会追加写入 `<dir>/.codexflow/<task_id>/history.jsonl`，便于按时间顺序回放整个流程。

示例配置：

```json
{
  "task_id": "feature-a",
  "goal": "完成一个最小闭环",
  "initial_role": "设计者",
  "max_turns": 12,
  "roles": [
    {
      "name": "设计者",
      "description": "负责设计和实现",
      "instructions": "根据目标推进实现，必要时修改代码并做最小验证",
      "prompt": "先完成第一轮实现",
      "allowed_next_roles": ["审核者"]
    },
    {
      "name": "审核者",
      "description": "负责审核结果",
      "instructions": "检查实现是否满足目标，并给出可执行反馈",
      "prompt": "审查最新结果",
      "allowed_next_roles": ["设计者"]
    }
  ]
}
```

启动方式：

```bash
go run ./cmd/codexflow --config ./feature-a.json --dir .
```

也可以使用绝对路径：

```bash
go run ./cmd/codexflow --config /Users/cc/opensource/CodexFlow/feature-a.json --dir /Users/cc/opensource/CodexFlow
```

运行逻辑：

- 从 `initial_role` 开始
- 每轮执行当前角色的提示词
- 当前角色必须输出符合 JSON Schema 的结构化结果
- `reply_to_user` 和 `handoff_summary` 等关键字段不能为空
- 当 `allowed_next_roles` 有多个候选时，由大模型从候选列表中选择 `next_role`
- 当状态为 `continue` 时，程序跳转到 `next_role`，并在终端输出下一角色
- 当状态为 `blocked` 或 `complete` 时，流程结束，并在终端输出 `completion_reason`
- 最多执行 `max_turns` 轮
