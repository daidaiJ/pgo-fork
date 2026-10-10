# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：**只做指针与现场速览，不堆批次卡**
> （2026-10-11 用户拍板）。批次细节的唯一权威在 wiki——最新卡
> [wiki/port/handoff.md](wiki/port/handoff.md)、历史
> [handoff-archive.md](wiki/port/handoff-archive.md)、任务详录
> `plan/phase-1..8.md`；本文件每批只回填速览表一行 + 当前阶段指针。
> L0 入口见 `wiki/README.md`。

## 🔄 现场速览

- **当前阶段**：**T8.2 LSP 批 2 已落地**（2026-10-11 晚，feat `9b71cf7` +
  docs `fcc0f0f` 推送 origin/dev；44 包 0 FAIL）——B1/B3/B4 落地 +
  B2 watched_files 取证改判不落地（D-12），余 B5 随 T7.6+D-C1；第 8 期
  ✅ 全部完成 4/4。最新交接卡 =
  [wiki/port/handoff.md](wiki/port/handoff.md) 2026-10-11（晚）。
- **接手者第一步**：按排期口径（实用体验 > 小添头）从待排期池取件——
  T7.6+D-C1、T7.1、T7.2、O1（与 S3/S4 合并）、MCP 默认 deferred 补齐
  （D-11）、dynamic-workflow（候选 C + 硬路由）、T6.3、T6.6、T7.4；或等
  用户 TUI 真机验收反馈（清单 = `.handoff/handoff.md` §2.2 + 各面板）。

### 近期批次速览（细节 = wiki 卡/分册，勿在此展开）

| 日期 | 批次 | commit | 规格（wiki/port/） |
|------|------|--------|--------------------|
| 10-11 晚 | T8.2 LSP 批 2（查询面 + per-tool 开关 + pull 兜底 + 收割重放 + 自动安装；B2 改判不落地） | `9b71cf7`+`fcc0f0f` | lsp-support.md §7 |
| 10-11 | T8.3 输入排队（队列 pane + 中断冻结 + Alt+Enter send-now） | `5668f49`+`fdf60c8` | queue-management.md |
| 10-11 | T8.4 /shell 切换 + 默认 shell 后端 | `c84ce54`+`73dd287` | shell-switch.md |
| 10-10 深夜 | T8.2 LSP 批 1（内核 + overlay 回灌 + /lsp + 配置分层 trust 门） | `655ed86`+`2393ee5` | lsp-support.md |
| 10-10 晚 | T8.1 供应商配置统一（~/.pigo 归一 + [provider] 节 + 两级代理） | `15b9d4e`+`5e943f5` | provider-config.md |
| 10-10 | T7.7 命令面契约重构收口（四切片：别名 + 受众门 fail-closed） | `c752368`+`0f727f4` | slash-command-surface.md |
| 10-09 | T7.8 请求转储（失败 provider 请求自动落盘 + /dump） | `e55fc51`+`8f821ae` | reqdump.md |
| 10-08 | T7.3 交互式 slash 面（/model 链式 + /skills //mcp 两级面板；三段提交，真机验收待用户） | `3509800`/`0936716` | tui-slash-ux.md §8–11 |

更早批次与全量脉络：[handoff-archive.md](wiki/port/handoff-archive.md)
（只追加、不改写）。

## 未验证事项（真机验收与缓落件）

- [ ] **TUI 面板真机手感一批约**：T8.3 队列 pane（↑↓ 选中 + Del + Esc 冻结
      体感 + Alt+Enter 各终端序列）、T8.4 /shell 面板、T8.2 /lsp 面板（含批 2
      工具行 Space 翻转）；前置各自见规格（/lsp 需 `[lsp] enabled` 或项目层
      开 + 目录受信）
- [ ] **T7.3 面板族真机对表 grok**（/model 档案链式、/think 下拉、/skills
      //mcp 两级、slash 菜单 [skill] 标记、/resume 别名；前置 = config 先加
      `[models."<id>"]` 档案；清单 = `.handoff/handoff.md` §2.2）
- [ ] **T8.2 批 2 余项**：B5 = lsp_rename 过 T5.2 + 多 root/多 server 路由
      （随 T7.6+D-C1 取件）；call hierarchy 缓；watched_files 重开条件 =
      多语言 server 出现（D-12）；auto-install 冷装路径未真机走通（单测钉边界）
- [ ] **T7.8 `/dump` 的 TUI 面**；流式开始后的失败不落盘（登记口径）
- [ ] **T7.7 缓落**：`Suggest`/`Preselect` 与 `Offered` 谓词、9 个 REPL-face
      命令的 TUI 面板化（独立批）、受众门生产消费面接入
      （slash-command-surface.md §7）

## 指针（权威全在 wiki）

- **交接台账**：[wiki/port/handoff.md](wiki/port/handoff.md) 只留最新卡，
  旧卡整体入 archive；新会话先读最新卡，再 `git log` + `git status` 核对
  现场，禁止凭记忆续写。本地现场卡 = `.handoff/handoff.md`（gitignored，
  收口计划与验收清单在其 §2）。
- **施工权威**：[implementation-plan.md](wiki/port/implementation-plan.md)
  （索引 + 期级状态总览，判断"这期完没完"免开分册）+ 任务详录
  `plan/phase-1..8.md` + `plan/backlog.md`。
- **施工纪律**：[design-principles.md](wiki/port/design-principles.md)
  （R1–R12；R11 = 多参照择优，R12 = 交互形态是命令的第一身份）。
- **测试基建**：新测试一律 `testenv.Dir(t)` 而非 `t.TempDir()`（Windows
  TempDir 清理假报警已断根，2026-10-07）。
- **长期口径（2026-10-06 用户点名）**：MCP 与 code mode 是后续确定方向，
  排期按「前置依赖」分层，不用「不做」封死；MCP 工具默认进 deferred/hidden
  面（实测全物化 +5435 token/轮）；code mode 须先钉死沙箱边界与 T5.2
  副作用契约。
- **T1.1 规格**：[startup-exit-probes.md](wiki/port/startup-exit-probes.md)
  （span 名以 §2.2 埋点表为唯一权威，已含实现偏差登记）。

## 环境事实

- `wiki` 是指向 `D:\wiki\pandawiki\projects\pgo-fork\wiki` 的符号链接，已在
  `.gitignore`（`/wiki`）——**wiki 内容不进 git**，改 wiki 即改 pandawiki 侧文件。
- Windows 开发机：`go build` 间歇被卡巴斯基锁 `%TEMP%`，构建统一加
  `GOTMPDIR=<repo>/.gtmp`（R9）——**只用于 go build**：go test 不受卡巴
  斯基影响，加了反而让 `t.TempDir()` 走 GOTMPDIR 产出混合斜杠路径打破
  trust 测试；全量测试后台跑、输出落文件（R6）。**`GOTMPDIR` 必须传原生
  Windows 路径形态**（`GOTMPDIR='D:\CODE\ai\pgo-fork\.gtmp'`）；传
  `/d/CODE/...` 这种 MSYS 形态会被 Go 拒绝（`creating work dir:
  GetFileAttributesEx ...: The system cannot find the path specified`）。
- 本地两份二进制 `./pigo.exe` 与 `./bin/pigo.exe`（gitignored）：**重建时
  build 一次 + `cp` 刷第二份**，否则用户可能跑到旧件（10-08 曾因 bin 停在
  旧版本导致「实测跑的是旧二进制」）。
- dev 分支本地开发，**2026-10-09 起按用户指示推送 `origin/dev`**（daidaiJ/pgo-fork
  fork 仓）；上游同步 / PR 策略已拍板（2026-10-11）：维持现状，只推 origin/dev。
