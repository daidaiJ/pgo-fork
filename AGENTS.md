# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**第 2 期代码四件全清（T2.1 shellguard + T2.2 流式
  markdown checkpoint + T2.3 RemapANSI16 + T2.4 toolcard 注册表化，
  2026-10-05）**，仅余第 2 期对账：期验收的 256/16 色截图回归补账（无头
  环境截图途径待拍板）+ 危险命令表驱动全绿确认 → 打 dev 预览 tag。
  T2.4 = toolcard.go 拆四渲染器注册表（bash/file/edit 折叠细节 +
  generic 兜底，crush tools.go 工厂模式 pigo 化）+ 渲染缓存基元 +
  expandable 能力接口，偏差五条见 tui-crush-components.md §11；
  T2.3 偏差六条见 §10（Theme.ANSI 16 槽调色板、窄宽度 <20 列平铺、
  随件修复 T2.1 headless 终止竞态见 shellguard 规格 §7.11）；T2.2
  偏差九条见 §9；T2.1 = 三驱动接线**默认 off（用户拍板）**，偏差十一条
  见规格 §7。
  pi-durable 切片 1、/ui 面板、skill-as-tool、供应商韧性 S 件族仍待拍板；
  2026-10-05 用户点名新增待议：dynamic-workflow 编排器（原"明确不做"改判）、
  code mode（goja 起步、不引 QuickJS）——见 implementation-plan 观察池
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
  `GOTMPDIR=<repo>/.gtmp`（R9）；全量测试后台跑、输出落文件（R6）。
- dev 分支本地开发，推送/PR 策略待用户拍板。
