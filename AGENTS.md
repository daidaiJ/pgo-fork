# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**T3.4 canonical context edit 完成（2026-10-06，当日落地，
  四 commit）**——`541289a`（S1 agentcore `ContextEditMessage` 编辑条目：
  编辑即历史，随树持久/重放幂等/分支继承）→ `b3c570e`（S2 投影第四级 +
  **随件修复 D-6**：投影全程 view→raw 映射 `ProjectViewMapped`/`ViewRawOf`
  替换漂移的 marker 锚公式）→ `a4e4c82`（S3 `context_edit` 工具：call-id/
  seq 双句柄，loop 注入 AgentContext 零驱动接线）→ `10525c7`（docs）。
  全量 38 包 exit=0。规格偏差 D-1/2/3/5/6 登记 =
  **wiki/port/canonical-context-edit.md §8**。下一步 = 第 3 期收口评估
  （期验收 = 真实会话 token 曲线留档 + rewind 全闭环回归）；T3.1 遗留
  flake（TestREPLExportImportRoundTrip 整包偶发）未动。待拍板项
  （**T3.3.1 压缩策略管线抽象——用户点名，趁热切片 0.5–1d**、pi-durable
  切片 1、/ui 面板、skill-as-tool、供应商韧性 S 件族、dynamic-workflow、
  code mode、TUI 风格对齐 grok——**风格迁移需求已按用户截图提取为
  tui-grok-style.md，14 条 S 需求待拍板排期**）见 implementation-plan
  待议段；其余均见 handoff。
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
