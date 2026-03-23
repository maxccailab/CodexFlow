# CodexFlow

CodexFlow 是一个用 Go 编写的本地多角色串行编排器。

它会读取一个 JSON 配置文件，按角色顺序调用本地 `codex` 命令行程序，并根据每一轮返回的结构化结果决定继续、阻塞或完成。

## 项目结构

```text
.
├── cmd/
│   └── codexflow/
│       ├── main.go
│       └── main_test.go
├── docs/
│   └── 项目说明.md
├── examples/
│   └── todo-api.json
└── go.mod
```

## 使用方式

### 1. 安装

在项目根目录执行：

```bash
go install ./cmd/codexflow
```

安装完成后，确保 Go 的可执行文件目录已经加入 `PATH`。

### 2. 运行程序

```bash
codexflow --config ./examples/todo-api.json --dir .
```

参数说明：

- `--config`：任务配置文件路径
- `--dir`：工作目录

安全提示：

- 当前版本会默认以 `--dangerously-bypass-approvals-and-sandbox` 调用本地 `codex`
- 这意味着 `codex` 会以高权限模式直接访问当前工作目录
- 对外部仓库、陌生代码或高风险任务，建议先把 `codexflow` 放在隔离沙箱、容器或临时工作区中运行，再决定是否接入真实项目目录

如果你还没有安装到 `PATH`，也可以直接在项目根目录执行：

```bash
go run ./cmd/codexflow --config ./examples/todo-api.json --dir .
```

### 3. 运行测试

当前测试不直接调用真实 `codex`，而是通过 mock 和纯函数校验验证逻辑。直接在仓库根目录执行：

```bash
go test ./...
```

## 说明

- 详细设计见 [项目说明.md](./docs/项目说明.md)
- 示例配置见 [todo-api.json](./examples/todo-api.json)
