# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 6 期第五批：T6.10 TUI 语义级别渲染对齐 grok，实作落地
  （2026-10-07，代码入 dev 待推）**——契约 C1–C5（display_mode 统一折叠 /
  轮次归属+历史轮 dim / 块级渲染缓存 / 布局区域序 / blockMeta 时间戳）+
  S1–S14（页眉、用户背景带、单行菱形工具行+卡牌退役展开态、thinking
  ◇/◆、运行状态行、圆角输入框+model·审批标签、usage 行、键位行）+
  **/context 上下文面板**（用户补图驱动：面板语义 + 菱形网格 + 分解行 +
  Tab 会话信息；Dashboard 其余 tab 留待）。42 包 0 FAIL。规格 =
  [tui-render-semantics.md](wiki/port/tui-render-semantics.md)（§10 偏差
  D-1~D-10）；真彩终端截图回归待用户实测核验。
  上一批 T6.8 MCP + T6.9 slash 已提交（`d9cc9fe`，2026-10-07 复核 0 FAIL）。
- **接手者第一步（待用户拍板）**：①TUI 真彩截图回归核验（对照
  tui-grok-style §1 截图，同第 2 期收口流程）；②**T6.4 pstack 精选 4+3
  仍未拍板**（清单 =
  [pstack-skill-inventory.md](wiki/port/pstack-skill-inventory.md)）。
- **长期口径（2026-10-06 用户点名）**：**MCP 与 code mode 是后续确定方向**
  ——盘点与排期一律按"前置依赖"分层，**不要用"不做"把件永久封死**（依赖前置
  表见 `wiki/port/pstack-skill-inventory.md` §5）。两条硬约束：MCP 工具默认
  进 deferred/hidden 面（不得默认 direct，实测 18 个 model-invocable 内置
  全物化已 +5435 token/轮）；code mode 须先钉死沙箱边界与 T5.2 副作用契约，
  脚本不得比工具拿到更宽的口子。
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
