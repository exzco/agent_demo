# ACode CLI (acode) - 生产级终端 AI 编程智能体

ACode CLI (`acode`) 是一个采用纯 Go 语言构建的终端原生（Terminal-Native）高性能 AI 编程智能体系统。专为专业研发场景打造，全面复刻了工业级 Coding Agent 的核心控制闭环，并深度融合了 **DeepSeek / OpenAI 级前缀缓存（KV Cache）保持优化**、**自适应滑动窗口上下文压缩**、**隔离子代理并发编排**、**多级安全沙箱权限控制** 与 **ANSI Unified Diff 终端人机交互**。


## 📁 目录架构说明

```text
acodeRawCode/
├── cmd/
│   └── acode/                        # 主入口程序与系统级集成测试
│       ├── main.go                 # 交互式 CLI、参数解析与 Agent 装配流水线
│       ├── agent_test.go           # 多步工具调用与保护钩子测试
│       ├── advanced_test.go        # Subagent、Context 压缩、Customizations、TUI 测试
│       └── keybindings_test.go     # 77 项快捷键真值对拍测试
├── internal/
│   ├── agent/                      # 智能体核心控制循环（ReAct 循环、事件流分发与上下文跟踪）
│   ├── cli/                        # 终端监听器、工具折叠渲染与格式化输出
│   ├── context/                    # 滑动窗口长会话压缩引擎与 /compact 检查点
│   ├── cortex/                     # 步骤状态机与推演模型
│   ├── customizations/             # 技能自动注入（SKILL.md）与 .acodeignore 过滤规则
│   ├── encoder/                    # CDP 截帧解析与 PNG 分帧无损编码器
│   ├── hooks/                      # 终止守卫：步数上限截断与死循环检测钩子
│   ├── llm/                        # SSE 流式 HTTP 网关、429 指数退避重试客户端
│   ├── logo/                       # 终端 ANSI 彩色启动 Banner
│   ├── permission/                 # 4 级安全沙箱门禁（readonly/workspace/full/confirm）
│   ├── repl/                       # REPL 交互会话、52 项斜杠命令处理器与配置中枢
│   ├── settings/                   # 本地持久化配置 (~/.acode/settings.json) 原子存储
│   ├── subagent/                   # 隔离子代理管理器、生命周期与 invoke_subagent 工具
│   ├── tools/                      # 4 大原生执行器：Bash, Write, View, Replace（带 Spill 缓存保护）
│   ├── trajectory/                 # 会话轨迹记录与持久化
│   ├── tui/                        # ANSI 状态栏、77 键提示条、Unified Diff 审阅器与下拉选择菜单
│   └── types/                      # 跨模块共享数据模型与协议类型
├── test/                           # 跨包端到端与稳定性集成测试 (Deterministic Spill, Randomness)
├── docs/                           # 架构设计、特性手册与开发文档
│   ├── features.md                 # /model, /permission, /settings 深度特性手册
│   ├── agent_dev.md                # 2900+ 行全模块源码逐行解析与设计内幕
│   └── dev.md                      # KV Cache 缓存调优记录与规范
├── notes/                          # 官方真值对照数据（77 项快捷键清单）
├── go.mod                          # Go 模块定义文件 (module acode)
└── README.md                       # 本架构说明文档
```

---

## 📖 核心命令与特性手册

关于系统的详细设计、参数范式与交互逻辑，请参考专用特性手册：
- **[核心特性手册：/model · /permission · /settings](docs/features.md)**：包含模型热重载流程图、4 大沙箱模式矩阵与配置覆盖优先级。

### 常用斜杠命令速查

| 斜杠命令 | 语法示例 | 说明 |
| :--- | :--- | :--- |
| **`/model`** | `/model gemini-2.5-pro` | 切换激活模型并自动联动重校准上下文预算 |
| **`/permission`** | `/permission workspace` | 切换沙箱模式（`readonly` / `workspace` / `full` / `confirm`） |
| **`/settings`** | `/settings url <url>` | 统一配置面板（支持交互菜单与终端密钥无回显输入） |
| **`/compact`** | `/compact` | 手动强制压缩当前对话历史，固化摘要检查点 |
| **`/goal`** | `/goal <instruction>` | 目标驱动模式，持续自主推进任务直至完全达成 |
| **`/status`** | `/status` | 查看当前会话 ID、已耗步数与 Token 明细消耗 |

---

## 🛠️ 构建与快速上手

### 1. 编译二进制文件
```bash
go build -o acode ./cmd/acode
```

### 2. 交互式 REPL 模式（推荐）
启动后自动载入本地 `~/.acode/settings.json` 配置，进入带高亮与补全的终端交互环境：
```bash
./acode
```

### 3. 单次任务模式 (-p)
适合脚本调用与自动化流水线：
```bash
# 单次指令直接执行并输出
./acode -p "检查当前目录下的 Go 代码规范并输出整改建议"

# 开启全权模式（容器内无人值守）
./acode -yolo -p "修复当前目录所有的单元测试报错"
```

