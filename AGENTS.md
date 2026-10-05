# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 2 期进行中——T2.1 shellguard 已落地（2026-10-05）**，
  余 T2.2 流式 markdown checkpoint / T2.3 RemapANSI16 / T2.4 toolcard
  注册表化。T2.1 = `internal/shellguard` 纯叶子包（mvdan/sh AST，~110 翻译
  用例全绿）+ `internal/agenttool/shellguard.go` seam + 三驱动接线 +
  headless `--non-interactive-denial`；**默认 off（用户拍板：可配置启用的
  高级特性）**，`[shellguard] mode`/`--shellguard`，偏差十条见规格 §7；
  pi-durable 切片 1、/ui 面板、skill-as-tool、供应商韧性 S 件族仍待拍板
  ——均见 handoff。
- **交接台账（L0.5 必读，只追加）**：[wiki/port/handoff.md](wiki/port/handoff.md)
  ——新会话先读最新条目，再 `git log` + `git status` 核对现场，禁止凭记忆续写。
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
  `GOTMPDIR=<repo>/.gtmp`（R9）；全量测试后台跑、输出落文件（R6）。
- dev 分支本地开发，推送/PR 策略待用户拍板。
