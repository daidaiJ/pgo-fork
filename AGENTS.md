# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 5 期收口验收完成 + T4.1 期验收 1（token A/B）收账 +
  Windows 插件 bug 修复（2026-10-06，`300e2b1`/`ff100d8`）**——
  ①T5.2/T5.3 收口验收过目 ✅：T5.2 行为变化知悉（判定序/自改面/deny 终局/
  plugin 受 gate/fail-closed，实跑活样本两例），skill 双件实跑达标
  （/handoff 产物合规并揪出 AGENTS.md 工期笔误已修；/defect-detective
  行为达标）；②**T4.1 token A/B ✅**：hello 单轮实测 direct 9002 /
  deferred 8561 inputTokens，净省 441 tok（插件面 811 tok 的 54%），
  provider 确认 schema 计入 usage——deferred 档价值实证，数字见
  implementation-plan T4.1 行；③**随实跑修复 Windows 插件加载 bug**
  （`300e2b1`）：`internal/plugin` `isExecutable` 只看 execute 位 →
  Windows os.Stat 对 .exe 报 0666 → Discover 静默跳过全部插件；修复 =
  os/exec LookPath 式扩展名判定 + 单测锁行为。
  **下一步待用户拍板**：第 6 期立项排序（**skill-as-tool 完整体
  ~1–1.5d 首位**，T5.4 结论；handoff 产物建议首批 = skill-as-tool +
  T6.7）；小改进项：handoff SKILL.md 补"自改面清单以 selfedit.go 为准"
  （防实跑中出现的模型幻觉）；pigo 记忆库污染现象（两次实测）可议立项。
  其余遗留与待议段小件见 implementation-plan 待议段；其余均见 handoff。
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
