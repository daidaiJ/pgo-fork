# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：第 1 期进行中——**T1.1 启动/退出探针已完成**（`internal/spans`
  + 全链埋点，tip `73220ea`，全量测试绿）；2026-10-04 完成七轮规划审查
  （覆盖面/六面/后台监视器/fork 自有特性两轮/slash 全量差集+压缩潜藏设计
  +todo 查看面/六原型可交互面并行摸排/记忆系统专项，结论见 handoff 最新
  条目），待拍板：T1.2 / T1.3 / pi-durable 切片 1（headless 续跑）；
  skill-as-tool 完整体、供应商韧性 S 件族（六面审查候选）；周额度下界反推
  （随 T5.4，用户点名）——均见 handoff。
- **交接台账（L0.5 必读，只追加）**：[wiki/port/handoff.md](wiki/port/handoff.md)
  ——新会话先读最新条目，再 `git log` + `git status` 核对现场，禁止凭记忆续写。
- **施工权威**：[wiki/port/implementation-plan.md](wiki/port/implementation-plan.md)
  （六期任务表，状态每期收口回填；T1.1 已 ✅）。
- **施工纪律**：[wiki/port/design-principles.md](wiki/port/design-principles.md)
  （R1–R10；附录已登记 7 个 Windows 测试差异族 + `internal/testgate` 门控机制，
  本机全量测试基线 = 门控后全绿）。
- **T1.1 规格**：[wiki/port/startup-exit-probes.md](wiki/port/startup-exit-probes.md)
  （span 名以 §2.2 埋点表为唯一权威，已含实现偏差登记）。

## 环境事实

- `wiki` 是指向 `D:\wiki\pandawiki\projects\pgo-fork\wiki` 的符号链接，已在
  `.gitignore`（`/wiki`）——**wiki 内容不进 git**，改 wiki 即改 pandawiki 侧文件。
- Windows 开发机：`go build` 间歇被卡巴斯基锁 `%TEMP%`，构建统一加
  `GOTMPDIR=<repo>/.gtmp`（R9）；全量测试后台跑、输出落文件（R6）。
- dev 分支本地开发，推送/PR 策略待用户拍板。
