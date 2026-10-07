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
  Tab 会话信息；Dashboard 其余 tab 留待）+ **欢迎页重刷**（banner 对表
  grok views/welcome：灰调 logo + 版本徽章/副标题 + 点引导菜单行 + tip）。
  42 包 0 FAIL。规格 =
  [tui-render-semantics.md](wiki/port/tui-render-semantics.md)（§10 偏差
  D-1~D-10，§10.12 = 打磨批）；真彩终端截图回归待用户实测核验。
  T6.10 主批 `1378131` + banner 重刷 `2c21725` + 打磨批 `cac34bb`（cache
  0%/think %/todo 渲染器/去 ATX/grok 提示语法/banner 居中）+ 指针 `c3f9bbb`。
  **2026-10-07 晚追加：现场三缺陷批 `6787775`**（42 包全绿）——
  A Thought 下空白带（供应商纯空白文本，transcript 三层防御 + renderAll
  跳空块）、B header 上下文读数首帧不显示（withSession 播种 + turnEndMsg
  每轮刷新）、C **TUI bash 权限静默拒绝**（TUI 从不 EstablishTrust，
  `--approve` 没进引擎 trustedFn → 无持久化信任时连只读 `git log` 都被
  unpaired-remote ask 通道 fail-closed 且文案误导"用户拒绝"；最小修复 =
  trustedFn 纳入 opts.Approve）。C 的设计级待拍板（本地审批面板 / 拒绝
  文案诚实化 / bash per-command 只读快路径）= D-C1~D-C3，登记在
  [tui-blank-header-fixes.md](wiki/port/tui-blank-header-fixes.md) §5.4。
  **2026-10-07 晚交互批 `e404a3e`**：①鼠标点击折叠/展开——renderAll 行→块
  命中表（hits），点块首行（折叠菱形行/◆ footer/展开卡标题）即切换该块，
  Ctrl+O/Ctrl+T 状态机抽为 toggleBlock 共用；正文行仍是文本选择区，
  /context 面板打开时跳过。②todo 展开卡对齐 grok 任务列表：`Tasks · n/m
  done` + ▸进行中/✓完成(绿+灰退)/◇待办，替换 generic 卡（旧路径 JSON
  dump+checkbox 重复）。规格 = [tui-mouse-fold-todo.md](wiki/port/tui-mouse-fold-todo.md)
  （偏差 B-1 todo 第二态无视觉差、B-2 点击仅块首行）。42 包 0 FAIL。
- **接手者第一步（待用户拍板）**：①TUI 真彩截图回归核验（对照
  tui-grok-style §1 截图，同第 2 期收口流程）；②**T6.4 pstack 精选 4+3
  仍未拍板**（清单 =
  [pstack-skill-inventory.md](wiki/port/pstack-skill-inventory.md)）；
  ③**T7.1 子智能体中断续接：R11 参照调研已完成（2026-10-07 深夜），
  定案 = 融合分层**——A 做底座（task 工具 resume 参数 + 历史落盘前缀
  重放，grok resume_from + opencode task_id 参照）、B 降级为超窗策略
  （crush 摘要重入队参照）、C 进度清单作重派增强；取证与登记 =
  [subagent-resume.md](wiki/port/subagent-resume.md) §2.5，**实作排期
  待拍板**；④**T7.2 供应商自定义请求头 + opencode go 兼容头**（取证 +
  设计候选 A/B/C/D 已齐 =
  [provider-headers.md](wiki/port/provider-headers.md)，落地排期待拍板）；
  ⑤**T7.3 slash 小功能批 + T7.4 主题系统已立项（后续会话实作）**——
  /model /rename+终端标题 /usage /stats /context /memory /recap /sessions
  交互设计 + 原型配色吸收，grok/qwen 双参照调研 + S1-S9 定稿 =
  [tui-slash-ux.md](wiki/port/tui-slash-ux.md)，切片排期待拍板；
  **2026-10-07 晚用户实测点名：现状 /sessions /model 的显示与 slash 交互
  和 grok 完全不同——验收以 grok 实机交互逐条对表，S1+/sessions 件提
  本批最前**；⑥T7.5 grok 实用功能移植批已立项（全量普查 =
  [grok-command-inventory.md](wiki/port/grok-command-inventory.md)，
  P1 清单待排期）；⑦farewell 彩蛋已落地（🐼 Code together, cola
  together 退出行）+ header 右段让位滚动条列已修（42 包 0 FAIL）；
  ⑧**T7.6 审批模式三态批已立项（plan / ask / 全部允许的显示 + 切换
  途径，用户点名现状缺失）**——对接 T5.2 trusted/ask seam，**强依赖
  D-C1 本地审批面板**，建议合并设计，登记 =
  [implementation-plan.md](wiki/port/implementation-plan.md) 第 7 期。
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
