# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 6 期首批实作收口：T6.5 skill-as-tool 完整体 +
  T6.7 截断挽救（2026-10-06，`5f8a1e8`/`19df959`/`58d940a`，全量测试
  全绿）**——
  ①**T6.7 截断挽救**：`internal/provider/salvage.go` grok 默认态
  （CompleteToolCalls）——length 停 + 全部 tool_calls 完整 → 改写
  stop=tool_use 丢弃残尾；openai/anthropic 两解码器 finishDone 接线；
  ②**T6.5 skill-as-tool**：技能物化为子 Agent 工具（可失败工厂
  `NewRunConfigE`，spawn 期失败走信封 D-7）；frontmatter `model` 消费 =
  子代理重解析 provider/凭证（铁律两条兑现）；子面 = ChildToolSet +
  共享信号量；README 已对齐。**遗留**：skill-as-tool 未真链路实跑（19
  内置技能均无 frontmatter model，钉模型路径待低成本实测——sensenova
  flash-lite + 隔离 PIGO_HOME）；skill 工具面 token 成本（18 技能全物化）
  与 T4.1 deferred 理念的张力可议。
  **下一步待拍板**：T6.5 实跑验收方式；第 6 期第二批排序（上卡建议 =
  T6.3 usage MVP + T6.1 写租约，T6.6 最后单独收）；记忆库污染现象立项
  与否。其余见 handoff。
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
