# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**T5.2 工具副作用契约 + 权限规则层实作落地（2026-10-06，
  第 5 期第二件，原 T5.1+T5.2 打包件当日收）**——内核三件：
  ①`agentcore.ToolEffect` 契约（`effect.go`：ReadOnly/Destructive/Scope/
  Timeout，可选接口 EffectAware，未声明=保守默认；21 内置工具全量声明+
  快照测试；executor 执行阶段 WithTimeout 消费）；②`internal/toolrules`
  叶子包（Rule/Store=permissions.json/词边界前缀 Match/**Engine 七步判定
  次序**/SelfEdit symlink 穿透/AskPort 零 IO seam）——deny 规则终局
  （trusted/bypass 不放行）+ ProposedRule 沉淀（REPL `s` 选项）+ 自改面
  强制 ask（trust.json/permissions.json/config.toml 精确文件级）；
  `[permissions]` 配置表；③接线：REPL engine 取代 trust 直连（remote
  前置 RuleSeam 保 deny 终局）/TUI ask 走配对浏览器无浏览器 fail-closed
  （**行为变化**）/headless nil ask fail-closed 可绕行；goal/btw 保留旧
  seam（D-6）。规格 = [wiki/port/tool-rules-layer.md](wiki/port/tool-rules-layer.md)
  （六参照融合 + 偏差 D-1~D-10 = §7）。全量基线 **41 包 0 FAIL**
  （`.gtmp/t52-full2.txt`）。此前 T5.1 信封（`ebdf2e3`）、第 4 期四件均 ✅。
  **下一步待用户拍板**：T5.2/T5.1 收口验收；第 5 期余件（T5.3 内置 skill
  双件 1d + T5.4 路由评估 0.5d，建议连做收期）；T4.1 期验收 1（需 API
  key）；T6.5 候选池排序（随 T5.4 结论）；其余待议段小件见
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
