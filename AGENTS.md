# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 6 期第四批：T6.8 MCP 实作（含 D-2 HTTP 扩口）+ T6.9
  slash 交互面，双双落地（2026-10-06，代码在工作区未提交）**——
  ①`internal/mcp`（stdio + **最小 Streamable HTTP**，D-2 扩口：会话头 /
  SSE 解析 / 404 会话失效一次重握手）+ `[[mcp.servers]]` 两级开关
  （server `enabled` **高于** tool `disabled_tools`→hidden 档）+ 命名
  `mcp__<server>__<tool>` + 默认 deferred + `BuildPlanWithSources`
  （`mcp:<server>` Source 真值化）；**实测**：本地 cbm（stdio，4 工具）+
  8338（http，3 工具）端到端通过。②`/skills`（`[skills] disabled` 过滤
  收口 `run.LoadSkills` 单点）+ `/mcp`（server/per-tool 启停 + reload 双校验）
  + `/status` 提升进共享注册中心（TUI 也有了）扩三节；**配置写盘 =
  `config/persist.go` 文本补丁**（保注释，歧义拒写）。偏差：**D-7 生效时点
  = 下个会话**（声明面每 run 构建一次，实证修正 §2.3）/ D-8 TUI /status
  部分节 / D-9 CLI 镜像 deferred。任务表 T6.8/T6.9 已 ✅ 回填。
- **接手者第一步（待用户拍板，二选一）**：①审工作区 → 提交本批（建议拆
  T6.8 / T6.9 两个 commit）；②回到原拍板 = **TUI 语义级别设计渲染对齐 grok
  build proxy**（先出 `tui-render-semantics.md` 规格，参照物 =
  `wiki/prototype/grok-build-proxy-agent.md` +
  [tui-grok-style.md](wiki/port/tui-grok-style.md) +
  [tui-crush-components.md](wiki/port/tui-crush-components.md)）。
  **T6.4 pstack 精选 4+3 仍未拍板**（清单 =
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
