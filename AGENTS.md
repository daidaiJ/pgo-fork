# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**T3.3 施工前置完成：十维压缩跨原型对比（2026-10-06，只读
  未动工）**——七原型（qwen/zcode/minimax/grok/step/kimi/crush + pigo
  基线）十维全 file:line 实测对比，结论在
  context-compaction-comparison.md §5。三要点：①持久模型定稿 = append-only
  树 + marker entry + 请求组装投影（五家原型收敛，**推翻"qwen 原位占位"
  预设**，R11 互斥已登记）；②qwen"三层压缩"修正为两层；③B 表勘误
  （grok 无 InsufficientReduction，实为 degenerate 摘要地板）。**实锤
  pigo 基线 6 缺陷**（§5.4）：最重 = 二次自动压缩丢旧摘要 + PersistTurn
  压扁 v3 树毁弃分支（T3.1 G3 隐患实锤），均为 T3.3 必修前置。T3.3 范围
  按 §5.5 定稿（3–4d，含基线结构件），T4.4 触发线公式定稿（implementation-
  plan T4.4 行）。T3.1 遗留 flake 未动：TestREPLExportImportRoundTrip
  整包偶发（疑似 NewID 微秒碰撞），待排查。下一步 = T3.3 微压缩动工
  （前置已满足）；待拍板项
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
