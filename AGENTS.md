# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**T3.3 微压缩 + 压缩持久模型改造完成（2026-10-06，三切片
  当日落地）**——`f46e71b`（S1 前置结构件：marker-entry + ProjectView 请求
  投影，修基线缺陷①②⑤，PersistTurn 双侧删压扁分支、v3 树 append-only）→
  `f07d91b`（S2 微压缩主体：zcode 双闸门 + 白名单分组 + 256 门槛 + sticky
  MicrocompactMessage）→ `139ecad`（S3：压缩后活状态 reminder 重注入）。
  全量 38 包 exit=0。规格 = **wiki/port/micro-compaction.md**（偏差 D1–D6
  登记；D-1 切点锚用 KeptBefore 相对量替代 step 的 entry id、D-4 openai
  cache 桶不单列防双重计数）。T3.1 遗留 flake 未动：
  TestREPLExportImportRoundTrip 整包偶发（疑似 NewID 微秒碰撞），待排查；
  真实 provider 端到端 token 曲线待观察（期验收项）。下一步 = T3.4
  canonical context edit（在 T3.3 marker 模型上做）；待拍板项
  （pi-durable 切片 1、/ui 面板、skill-as-tool、供应商韧性 S 件族、
  dynamic-workflow、code mode、TUI 风格对齐 grok——**风格迁移需求已按
  用户截图提取为 tui-grok-style.md，14 条 S 需求待拍板排期**）见
  implementation-plan 待议段；其余均见 handoff。
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
