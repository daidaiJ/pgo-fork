# P0 基建：启动/退出耗时探针（可开关，慢启动慢退出可定位）

> 动机：pigo 启动链路组装点多（config → provider/credentials → tools/skills/
> plugins/memory/task → session load → TUI/REPL 首帧），慢启动或慢退出发生时
> 缺乏定位手段。先例：grok-build-proxy 内建 span 画像层（`xai-grok-telemetry/
> src/spans/span_profile.rs`，环境变量开关 + folded 格式），其排查实战记录
> `docs-local/startup-perf/README.md`（冷启动 ≈2.7–6.8s 定位到模型目录远端拉取
> 单点，就是靠这套探针）。

## 1. 需求

1. **默认关闭、零开销**：不开探针时无额外分配、无时间读取（atomic bool 短路）。
2. **可开关**：环境变量 `PIGO_SPAN_PROFILE_OUT=<目录>` 开启（目录内每进程一个
   文件，避免并发互踩）；可选 flag `--trace-startup` 打印到 stderr（快速目测）。
3. **覆盖启动与退出两段**：span 命名约定 `startup.*` / `exit.*` 前缀，
   解读只看前缀（常驻 loop 的 span 累计值大于墙钟属正常，同 grok 教训）。
4. **杀进程不丢画像**：grok 的实测坑是"画像只在优雅退出落盘，kill = 白跑"。
   pigo 版要求：信号（SIGINT/SIGTERM）路径也 flush；main 顶层 defer flush
   覆盖 panic 退出。
5. **机器可读**：folded 格式（`<span 树路径> <self-time 微秒>`，一行一 span，
   `sort -k2 -rn` 直接出热点榜）为主，可选 JSONL 事件流（`ts/name/dur/self`
   便于程序分析）。

## 2. 设计（pigo 侧）

### 2.1 新包 `internal/spans`

```go
// 语义：Begin/End 手工区间；parent 用显式 Begin 返回值或命名约定 "a.b.c"。
func Enabled() bool                       // env 一次性解析（sync.Once）
func Begin(name string) *Span             // 未启用返回 nil-safe 零开销 stub
func (s *Span) End()                      // 记录 total；self = total - 子 span 累计
func (s *Span) Mark(note string)          // 里程碑事件（如 "first frame rendered"）
func Flush()                              // 落盘 folded + JSONL；信号与 defer 都调
```

- self-time 记账：`Span` 持有子累计计数器，`End()` 时 `self = total - children`，
  与 grok folded 语义一致（按 span 树路径折叠累计）。
- 未启用路径：`Begin` 返回 `nil`，`(*Span).End()` 是 nil receiver no-op——
  调用点无需 if 判断。

### 2.2 埋点清单（按启动顺序）

| span 名 | 位置 |
|---|---|
| `startup.flag_parse` | `cmd/pigo/main.go` |
| `startup.config_load` | `config.LoadConfig` + `applyFileConfig` |
| `startup.mode_dispatch` | main 的各运行模式分支入口 |
| `startup.setup_env` | `run.SetupEnv` 总 span（REPL/TUI/headless 共用） |
| `startup.setup_env.provider` | provider 注册 + 凭据解析（OAuth 路径重点怀疑对象，对齐 grok 冷启动教训） |
| `startup.setup_env.tools` | 内置工具注册表构建 |
| `startup.setup_env.skills` / `.plugins` / `.memory` / `.schedule` | 各装配段 |
| `startup.session_load` | resume 路径 `Store.Load`/`LoadEntries`（大会话文件怀疑对象） |
| `startup.ui_init` | TUI model 构建到首帧（`Mark("first frame")`）；REPL 到首个 prompt 打印 |
| `exit.session_flush` | 退出时 PersistTurn/未落盘回合写回 |
| `exit.shutdown` | MCP/插件/后台任务收割与清理 |
| `exit.total` | 从收到退出意图到进程结束 |

约定：新增耗时可疑的装配段必须同步加 span；span 名进本表（唯一权威），
未登记的 `startup.*` 名视为拼写漂移。

### 2.3 输出

- `<PIGO_SPAN_PROFILE_OUT>/pigo-<mode>-<pid>-<ts>.folded` + 同名 `.jsonl`。
- fold 规则与解读一行脚本直接抄 grok 实战：
  `sort -k2 -rn *.folded | awk '{printf "%9.1fms %s\n", $2/1000, $1}'`，
  只看 `startup.*` / `exit.*` 前缀。

## 3. 验收标准

1. 默认路径零开销：`Enabled()==false` 时 `Begin` 不读时钟、不分配（bench 断言）。
2. 正常退出 / SIGINT / panic 三种退出路径均产出完整画像文件。
3. 冷热启动各采一次：能从热点榜直接读出最慢装配段（验收基线：报告
   top-10 span 与 `time pigo --version` 墙钟差值可对账）。
4. `--trace-startup` stderr 输出人类可读时间线（span 名 + 起止毫秒）。

## 4. 规模

C 级粗估：spans 包 0.5–1 人天 + 埋点接线 0.5–1 人天。建议作为 dev 分支
第一块落地的代码（后续所有特性的启动回归都有基线可查）。
