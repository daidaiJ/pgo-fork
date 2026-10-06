# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**T4.3 BTW 侧会话落地（2026-10-06，用户拍板先于 T4.2）**——
  `/btw` 侧线程从纯内存升级为**持久 peek 会话**：`peek_` purpose 前缀（不改
  schema，SchemaVersion 保持 3）+ `internal/session/peek.go`
  （`PeekSession` 落盘句柄 / `Store.LatestPeek` 跨进程 reopen / `List()`
  单点过滤 peek、`ListAll()` 放行）+ `cli.Host` 增 `Peek/SetPeek`（repl
  实现、TUI parity，`/fork`·`/clone`·`/import` 随 `lastBtw` 置 nil）+ btw
  包每轮落盘侧 Q&A（**不复制主背景**、主会话零写入）。随件修 `NewID` 微秒
  冲突（新增 `Store.uniqueID`，Import/Fork/OpenPeek 接线）——根治既有 flake
  `TestREPLExportImportRoundTrip`（经 worktree HEAD 基线对比确认为既有偶发）。
  偏差 D-1~D-9 登记 [btw-peek-session.md](wiki/port/btw-peek-session.md) §7。
  基线 **39 包 0 FAIL**（`.gtmp/t43-test.txt`）。
  此前 T4.1（`58aedec`）、T3.3.1（`1768d08`）、T4.4 均清。
  **下一步待用户拍板**：第 4 期余下 **T4.2 问卷（2–3d）**；可随时插入
  **T4.1 期验收 1（defer 档 token A/B，需 API key）**。
  其余待拍板项（pi-durable 切片 1、/ui 面板、skill-as-tool、
  供应商韧性 S 件族、dynamic-workflow、code mode、TUI 风格对齐 grok——
  tui-grok-style.md 14 条 S 需求待排期）见 implementation-plan 待议段；
  其余均见 handoff。
- **交接台账（L0.5 必读，2026-10-05 起瘦身版）**：
  [wiki/port/handoff.md](wiki/port/handoff.md) 只保留**最新一条交接卡**，
  历史条目在 [wiki/port/handoff-archive.md](wiki/port/handoff-archive.md)
  ——新会话先读最新卡，再 `git log` + `git status` 核对现场，禁止凭记忆
  续写；更早脉络查 archive。
- **施工权威**：[wiki/port/implementation-plan.md](wiki/port/implementation-plan.md)
  （六期任务表，状态每期收口回填；第 1 期已清空）。
- **施工纪律**：[wiki/port/design-principles.md](wiki/port/design-principles.md)
  （R1–R11，R11=多参照实现择优：能融合则融合、不能融合取相对最优并在规格
  登记互斥原因；附录已登记 7 个 Windows 测试差异族 + `internal/testgate`
  门控机制，
  本机全量测试基线 = 门控后全绿）。
- **T1.1 规格**：[wiki/port/startup-exit-probes.md](wiki/port/startup-exit-probes.md)
  （span 名以 §2.2 埋点表为唯一权威，已含实现偏差登记）。

## 环境事实

- `wiki` 是指向 `D:\wiki\pandawiki\projects\pgo-fork\wiki` 的符号链接，已在
  `.gitignore`（`/wiki`）——**wiki 内容不进 git**，改 wiki 即改 pandawiki 侧文件。
- Windows 开发机：`go build` 间歇被卡巴斯基锁 `%TEMP%`，构建统一加
  `GOTMPDIR=<repo>/.gtmp`（R9）——**只用于 go build**：go test 不受卡巴
  斯基影响，加了反而让 `t.TempDir()` 走 GOTMPDIR 产出混合斜杠路径打破
  trust 测试；全量测试后台跑、输出落文件（R6）。
- dev 分支本地开发，推送/PR 策略待用户拍板。
