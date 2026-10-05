# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 2 期收口 + 期验收两笔小账当日清账（2026-10-05）**
  ——T2.1–T2.4 四件全清、期验收对账完成（真彩档用户实测核验，16 色档
  豁免），滚动预览 tag `dev-preview` 已打。两笔小账已清（用户拍板"只清
  小账"，T3.1 后排）：① provider 报错误导 = `ResolveNamedProvider` openai
  协议分支驱动名硬编码 "openai"，修复为按 spec.Name 构建（`bac4147`）；
  ② write 卡多行 input 续行对齐值列（`b8425fc`）。测试陷阱新知：
  **GOTMPDIR 只用于 go build**——加给 go test 会让 `t.TempDir()` 产出混
  合斜杠路径打破 trust 测试（详见 handoff 最新卡 + R9）。下一步 = 第 3
  期 T3.1 rewind 补齐四件（2–3d，rewind-branch-undo.md），待用户排。
  T2.4 偏差五条见 tui-crush-components.md §11；T2.3 偏差六条 §10；T2.2
  偏差九条 §9；T2.1 = 三驱动接线**默认 off（用户拍板）**，偏差十一条 §7；
  小账清账记录 §12。pi-durable 切片 1、/ui 面板、skill-as-tool、供应商
  韧性 S 件族仍待拍板；用户点名待议：dynamic-workflow 编排器、code mode
  （goja 起步）、TUI 风格对齐 grok——见 implementation-plan 待议段；其余
  均见 handoff。
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
