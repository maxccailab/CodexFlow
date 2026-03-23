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
│   └── feature-a.json
├── testdir/
└── go.mod
```

## 使用方式

### 1. 运行程序

```bash
go run ./cmd/codexflow --config ./examples/feature-a.json --dir .
```

参数说明：

- `--config`：任务配置文件路径
- `--dir`：工作目录

### 2. 运行测试

按仓库约定，在 `testdir/` 目录内执行：

```bash
cd ./testdir
go test ../...
```

## 说明

- 详细设计见 [项目说明.md](/Users/cc/opensource/CodexFlow/docs/项目说明.md)
- 示例配置见 [feature-a.json](/Users/cc/opensource/CodexFlow/examples/feature-a.json)
