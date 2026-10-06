# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 5 期收官：T5.3 内置 skill 双件 + T5.4 角色路由评估落地
  （2026-10-06，T5.3 = `f9bda6e`）**——①T5.3：`handoff`（交接台账）+
  `defect-detective`（三轴深审）经 builtinskills 内置，manifest 新增
  **dev-workflow 集合**（16→18 个），SKILL.md 已做 pigo 环境适配
  （handoff 存项目根 `handoff/`；defect-detective 检索面改 pigo 工具 +
  与 review-it 划界），repl 真实链路测试断言补两命令；②T5.4（零代码
  书面件）：**结论 = 不独立立项 role 路由大件，per-role model 并入
  skill-as-tool 完整体**（frontmatter `model` 消费 + SkillTool 接通，
  实证 frontmatter model 已解析零消费 / SkillTool 机制 ready 无调用点）；
  **T6.5 候选池更新：skill-as-tool 完整体升首位**。评估 =
  [wiki/port/role-routing-assessment.md](wiki/port/role-routing-assessment.md)。
  此前 T5.2（`9fc0470`）、T5.1（`ebdf2e3`）均 ✅（41 包 0 FAIL 基线）。
  **下一步待用户拍板**：T5.2/T5.3 收口验收过目（自改面拒绝 + 沉淀免确认 +
  TUI/headless fail-closed 行为变化 + skill 双件实跑）；第 6 期立项排序
  （skill-as-tool ~1–1.5d 首批建议）；T4.1 token A/B 验收（需 API key）；
  其余待议段小件见 implementation-plan 待议段；其余均见 handoff。
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
