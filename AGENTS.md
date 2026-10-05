# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 3 期 T3.1 rewind 补齐四件完成（2026-10-05，`5119df1`）**
  ——G1 弃分支点进 /rewind 选择器（↩ 标注、可切回）、G2 prompt 回填输入框
  （REPL raw-mode 预填 + TUI SetValue）、G3 compaction 边界 ⚠ 预计算（树推导
  近似，kimi 双 reason 未逐字移植）、G4 TUI 入口（对话级，TUI 无快照
  journal）。共用层 `internal/cli/rewindpoints.go`，schema 未动。偏差八条见
  rewind-branch-undo.md §8。**新发现既有 flake**：TestREPLExportImportRoundTrip
  整包偶发（基线亦复现，疑似 NewID 微秒碰撞令 Import 覆盖原文件），待排查。
  下一步 = T3.2 runaway 哨兵 / T3.3 微压缩，待用户排；待拍板项（pi-durable
  切片 1、/ui 面板、skill-as-tool、供应商韧性 S 件族、dynamic-workflow、
  code mode、TUI 风格对齐 grok）见 implementation-plan 待议段；其余均见
  handoff。
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
