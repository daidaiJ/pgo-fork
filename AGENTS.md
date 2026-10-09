# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## 🔄 Handoff 摘要

### 原型专题文档批（rpiv-mono / pi-dynamic-workflow / dynamic-workflow 形态设计）— done（wiki 侧，无代码改动）

- **当前状态：** 按用户点名（`juicesharp/rpiv-mono` 的 packages +
  `pi-dynamic-workflow` 社区插件）新增 **2 篇原型深读 + 1 篇专题设计文档**，
  并登记进 prototype/README、cross-prototype-selection、implementation-plan
  待议段；**无代码改动**，工作区仍为 T7.3 四批未提交状态；本地 pigo.exe
  已按当前工作区重建（GOTMPDIR 原生路径形态，19.8s，`--version` 冒烟过）
- **关键证据：** 新增 [wiki/prototype/rpiv-mono.md](wiki/prototype/rpiv-mono.md)
  （A 级，Pi 插件集 15 包：Pi 扩展 API 面 + 声明式流水线引擎 rpiv-workflow
  的 JSONL 行型/单一 fold/循环族/判官面板/端口抽象与真并行 lane + 编排层
  rpiv-pi + 卫星包速查）、[wiki/prototype/pi-dynamic-workflow.md](wiki/prototype/pi-dynamic-workflow.md)
  （A 级，Pi 社区动态工作流三仓并列：Michaelliv 上游 / milanglacier 同名字仓 /
  QuintinShaw 最全仓；命名歧义已在文档头登记；7 项四家收敛 + 单家特化表）、
  [wiki/port/dynamic-workflow-shape.md](wiki/port/dynamic-workflow-shape.md)
  （**待议**专题设计：四参照形态坐标 + **grok 引擎首次取证**
  `crates/codegen/xai-workflow/`（Rhai 脚本 / MAX_HOST_CALLS=10000 /
  context edit 只改可见面 + 参与请求哈希链 / PauseKind 五分类 / stub host
  干跑校验 / 会话内 4 活跃 run）+ Go 侧三候选（建议 C = 声明式外层 +
  受限表达式 + 逃生舱）+ 落点与五切片分期 + 4 条待拍板问题）
- **详情指针：** [`wiki/port/handoff.md`](wiki/port/handoff.md) 2026-10-08 卡
  + [`.handoff/handoff.md`](.handoff/handoff.md) §3 批次⑤；参照 clone 在
  `D:\CODE\ai\{rpiv-mono, pi-dynamic-workflows-michaelliv,
  pi-dynamic-workflow-milanglacier, pi-dynamic-workflows-quintinshaw}`（不进库）

### T7.8 请求转储：失败 provider 请求自动落盘 + `/dump` 命令 — 已落地并推送

- **当前状态：** 实作 = **`e55fc51`**（2026-10-09；新增 `internal/reqdump` 包 +
  transport 三处捕获 + `agentLoop` 发布 session id + `/dump` 契约命令；按用户指示
  推送 origin/dev）；43 包 0 FAIL（`.gtmp/dump-test2.log`）；两份二进制已刷
  （第二份 = `cp`，sha256 一致）；真机（本地假端点 200/500）核验通过。
- **关键证据：** 连接期失败（传输错误 / 429·503·529 重试耗尽 / 4xx·5xx）旁路捕获
  原始请求（`GetBody` 读回）+ 原始响应，凭据按形状脱敏（header/查询参数名含
  key/token/secret/auth/cookie… → `<redacted>`），落盘
  `$PIGO_DUMP_DIR` → `$PIGO_HOME/dumps` → `~/.pigo/dumps` 下的
  `<session id>-<时间戳>/dump.json`（目录 0700 / 文件 0600 / 单体 256 KiB 截断标记）；
  **有 session 在飞时失败即自动落盘**；`/dump` 写/复用（同根不造重复目录）并打印
  路径，本进程无记录时指磁盘上该 session 最新 dump；session id 单漏斗 =
  `runtime.agentLoop`（首版误挂 `StartRun`，headless 绕过，真机抓出后修正）；
  测试 = reqdump 单测 + provider 捕获/自动落盘 + prompts `/dump` 四例 + headless
  拒绝钉 + 目录清单钉 29→30。**登记不做**：流式开始后的失败不可复原、不落盘。
- **详情指针：** 规格 = [wiki/port/reqdump.md](wiki/port/reqdump.md)；任务 =
  [implementation-plan.md](wiki/port/implementation-plan.md) **T7.8**；卡 =
  [wiki/port/handoff.md](wiki/port/handoff.md) 2026-10-09 深夜 2。

### T7.7 命令面契约重构切片 3 — 已提交（下一步 = 切片 4）

- **当前状态：** 切片 3 落地 = **`3a36a02`**（2026-10-09 深夜，17 文件〔新增 4 /
  删 1〕，本地 dev 不推；切片 1 = `35edcd2`、切片 2 = `4bb2321` 照旧）；42 包
  0 FAIL（`.gtmp/s3-final-test.log`）；两份二进制已刷（第二份 = `cp`，sha256 一致）；
  REPL slash 面 + headless 拒绝已用本地假端点（SSE）+ 管道配方真机核验。
- **关键证据：** 单一 `ProjREPLFace` 拆为 9 个面值（`ProjFork`…`ProjDream`）+
  `Projection.REPLOnly()`；**REPL 的 13 名 if 链删除**（改按 Projection 单
  switch，每面一个投影点；名字/参数切分单源 `runtime.SplitInvocation`，REPL /
  TUI / headless 共用）；REPL 对 TUI-face 命令打「候选列表 + usage + 明确提示」
  （新 `repl/slashproject.go`：/sessions //resume 列会话、/rename //context 给
  用法行，不实现假交互）；**headless `-p` 遇内建 slash 明确拒绝**（exit 2，
  身份判定 = 新 `prompts.BuiltinCatalog()`，不再把字面文本当 prompt 发模型；
  plugin/模板/技能/普通文本与 `/未知名` 照旧）；候选面单源渲染
  `prompts.FormatCommandLine` + 徽标单源 `SlashCommandSource.Badge()`（TUI 菜单 /
  /help / REPL 补全三处同源，builtin 无标记；删 REPL `formatHelpLine` 死码与
  TUI `sourceTag`，/help 退役 `(source: <tier>)`）；测试 = runtime
  split/badge/REPLOnly + prompts `help_test.go` + repl `slashproject_test.go` +
  headless `slashguard_test.go` + tui 菜单行型。
- **排期口径（2026-10-09 用户裁定）：实用体验 > 小添头和主题美化**——T7.7
  切片 4 等实用件排最前；T7.4 主题系统等美化/添头件排后（登记 =
  `.handoff/handoff.md` §3.5）。
- **详情指针：** 规格 + 偏差 = [slash-command-surface.md](wiki/port/slash-command-surface.md)
  §7 切片 3 段（切片 1/2 段照旧）；任务 = [implementation-plan.md](wiki/port/implementation-plan.md)
  T7.7 ⑨；卡 = [wiki/port/handoff.md](wiki/port/handoff.md) 2026-10-09 深夜。

### T7.3 交互式 slash 改造四批 — 已提交（TUI 真机验收待用户）

- **当前状态：** T7.3 交互式改造 v1 + 实测修复批完成（用户三条点名：
  /model 列表来源 = config `[models."<id>"]` 档案 / /skills 行 = 名称
  +截断描述 / /mcp 两级 server+tool 可看可切，定稿 = tui-slash-ux.md
  §11），**已按三段提交到本地 dev**：`3509800`（testenv 迁移 + 两处 skill
  测试的 XDG 隔离）/ `0936716`（交互式 slash 面，四批合一）/ 同批 docs
  指针段；**不推远端**。42 包 0 FAIL；`pigo.exe` 与 `bin/pigo.exe` 均已
  重建（22:13，哈希一致——此前 `bin/` 那份停在 10-07 13:56，即上轮
  「实测跑的是旧二进制」的来源）
- **关键证据：** /model 链式（list→effort 子列表，裸提交直进）+ 档案
  face（下拉/启动 applyModelProfile/切换 switchToProfile，grok
  [model."<id>"] 对齐）+ registry `/model <id> [effort]`（REPL 同享）+
  /think//effect 下拉 + /skills 面板（clipDesc 行型）+ /mcp 两级面板
  （Status.Tools + Space 启停 + Enter 展开）+ /resume 别名 + slash 菜单
  [skill] 来源标记 + /session 待并入 /status tab；改动面 = tui 7 文件
  + listpanel.go 新 + prompts 2 文件 + config models.go 新 + mcp.go +
  cmd/pigo + repl 测试 XDG 隔离
- **验收标准：** 真机对表 grok（档案下拉/切换手感 / 链式手感 / 面板
  toggle / mcp 两级启停 / 来源标记 / /resume）+ 上三批；**验收清单 =
  [`.handoff/handoff.md`](.handoff/handoff.md) §2.2**（前置：config 里先加
  `[models."<id>"]` 档案，否则 `/model` 只见 preset 兜底面）
- **同日用户裁定：** 本批复现了「slash 交互为了迁就无头模式而设计」的
  **反模式**（每个交互命令都带一条参数式旁路）→ 立项 **T7.7 多面命令契约
  重构**；并点名"交互设计 grok build proxy 这边明显更正确"，故**规格定稿 =
  [slash-command-surface.md](wiki/port/slash-command-surface.md)**（grok 双目录
  取证〔agent 侧 `BUILTIN_COMMANDS` 能力门/受众门/纯 `resolve`→类型化
  `BuiltinAction`；client 侧 74 个命令对象 `SlashCommand` 四族方法 +
  `CommandResult` 类型化 + `PassThrough` 组合〕→ pigo 融合为「单份声明目录 +
  TUI/REPL/headless 三投影器」+ 四切片 + 验收 + 偏差登记）。登记 =
  [design-principles.md](wiki/port/design-principles.md) **R12** +
  [tui-slash-ux.md](wiki/port/tui-slash-ux.md) §13（§13.5 grok 参照裁定）+
  [implementation-plan.md](wiki/port/implementation-plan.md) T7.7
- **详情指针：** [`.handoff/handoff.md`](.handoff/handoff.md)
  —— **`/.handoff/` 自 2026-10-08 起只保留这一个文件**：§1 现场核对 / §2 收口
  计划（验收清单 + 三段提交草案）/ §3 历史批次速查（thinking+S1+S8、S2、交互式
  改造、修复批、原型文档批）/ §3.5 已拍板口径 / §4 待拍板清单 / §5 环境事实

### 未验证事项
- [ ] **本批（2026-10-08）：dynamic-workflow 载体路线待拍板**（形态设计
      §8 四条问题：候选 A 嵌入脚本 / B 纯声明式 / C 声明式外层 + 受限表达式
      + 逃生舱〔建议〕；脚本引擎与 code mode 合并选型；排期；是否要
      "显式请求才起工作流"硬路由）
- [ ] **优化池 O1 待拍板（2026-10-08 用户点名）**：状态行统计口径对齐 grok
      默认配置 + 统计状态落**会话**作单一真相（供 /usage //stats 复用）+
      **计入 subagent 开销**；登记 =
      [implementation-plan.md](wiki/port/implementation-plan.md) 优化池 O1 +
      [tui-slash-ux.md](wiki/port/tui-slash-ux.md) §12（S3/S4 接口影响）
- [ ] 两篇新深读文档附录的「未抽验行号清单」（subagent 转述部分）按需复核
- [ ] /model 档案下拉 + 链式 + /think 下拉真机手感（档案切换跨网关 /
      两段 Enter / Esc / 删字弹回）
- [ ] /skills //mcp 面板真机（行型截断 / mcp Enter 展开 + Space server/
      tool 启停写 config + note 回显）
- [ ] slash 菜单 [skill] 标记 + /resume 别名 + `/model <id> <effort>` 参数式
- [ ] **T7.8 `/dump` 的 TUI 面**（打成系统块 + 路径可读）；自动落盘与 `/dump`
      已在 REPL/headless 真机核验；**流式开始后的失败不落盘**为登记口径
      （原始响应不可复原），流式失败只照原样上报
- [ ] 上三批：S2 终端标题、S1/S8、thinking 布局、交互式 v1 真机对表 grok
- [ ] **T7.7 多面命令契约重构（2026-10-08 立项；切片 1 = `35edcd2`、切片 2 =
      `4bb2321`、切片 3 = `3a36a02`〔均 2026-10-09，本地 dev 未推〕；下一步 =
      切片 4 别名解析（fail-closed）+ 受众门测试 + 全量回归收口）**：
      现状 = **假注册已清零** + `Projection` 面枚举（22 类，含 9 个 REPL 面）
      + TUI runSlash 与 **REPL 两条按名字分发的拦截清单均已归零**（声明驱动单
      switch）+ /memory //rebuild 契约化 + sessions/resume/rename/context/
      memory/rebuild 入目录 + REPL 对 TUI 面出「候选列表 + usage + 明确提示」+
      headless `-p` 对内建 slash 明确报错 + 候选面（TUI 菜单 //help / REPL
      补全）同源自 `prompts.FormatCommandLine`；
      `Suggest`/`Preselect` 与 `Offered` 谓词按偏差登记缓落（候选是前端投影的
      live 数据）；
      目标形态照 grok（用户裁定"grok 这边明显更正确"）：
      **规格定稿 = [slash-command-surface.md](wiki/port/slash-command-surface.md)**
      （单份声明目录 + 类型化 `Intent`/`Execute` + TUI/REPL/headless 三投影器 +
      四切片 + 验收；偏差登记含"为何不照抄 ACP 双目录"）；
      登记 = R12 + tui-slash-ux §13.5 + implementation-plan T7.7

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**2026-10-09：T7.8 请求转储落地（`e55fc51`，已推 origin/dev）**——
  连接期失败（传输错误 / 429·503·529 重试耗尽 / 4xx·5xx）旁路捕获原始请求 + 响应，
  凭据按形状脱敏，落盘 `<dump 根>/<session id>-<时间戳>/dump.json` 并在有 session
  在飞时自动落盘；`/dump` 写/复用并打印路径（无记录时指磁盘最新 dump）；
  `agentLoop` 发布 session id 作单漏斗（headless 首版漏掉，真机抓出）；
  43 包 0 FAIL（`.gtmp/dump-test2.log`）；规格 = wiki/port/reqdump.md，任务 = T7.8。
  上一阶段：
  **2026-10-09 深夜：T7.7 切片 3 落地（`3a36a02`，17 文件
  〔新增 4 / 删 1〕，本地 dev 未推）**——
  单一 `ProjREPLFace` 拆为 9 个面值（`ProjFork`…`ProjDream`）+ `REPLOnly()`；
  **REPL 13 名 if 链删除**（按 Projection 单 switch，每面一个投影点；切分单源
  `runtime.SplitInvocation`）；REPL 对 TUI 面打「候选列表 + usage + 明确提示」
  （`repl/slashproject.go`）；**headless `-p` 遇内建 slash 明确拒绝**（exit 2，
  `prompts.BuiltinCatalog()` 身份判定）；候选面单源 `prompts.FormatCommandLine`
  + 徽标单源 `SlashCommandSource.Badge()`（TUI 菜单 //help / REPL 补全同源，
  删 `formatHelpLine` 死码与 `sourceTag`）；42 包 0 FAIL
  （`.gtmp/s3-final-test.log`）；二进制两份重建；REPL 面 + headless 拒绝管道真机
  核验过（本地 SSE 假端点）。偏差 = slash-command-surface.md §7 切片 3 段。上一阶段：
  **2026-10-09：T7.7 命令面契约重构切片 1 落地（`35edcd2`，19 文件
  +1113/−603，本地 dev 未推）**——声明面 + `Intent` 17 类封闭集（纯数据可序列化）
  + `ResolveOutcome` 返 `SlashIntent` + prompts `Executor`；9 注册转契约命令、
  假注册 15→13、`RunManualCompact`/`WriteSessionSummary` 双份合一、REPL 删
  /compact //session 拦截、TUI 删 /status //session 拦截 + **/compact 转正**
  （幻影 10→9）；42 包 0 FAIL；二进制两份重建（第二份 = cp，用户指正）。
  偏差 = slash-command-surface.md §7 切片 1 段。上一阶段：
  **2026-10-08 深夜：T7.3 四批收口（三段提交到本地 dev）+ T7.7 立项
  并落第一步（幻影命令回归测试已钉 = `fe5079e`，幻影实为 10 个、TUI 拦截实为
  17 名，旧口径 7/14 已在 wiki 与本文件修正；docs 段 = `920a838`）**——`3509800`（testenv 迁移 + 两处 skill 测试 XDG
  隔离）/ `0936716`（T7.3 交互式 slash 面，四批合一）/ docs 指针段；**TUI
  真机验收仍待用户**（清单 = [`.handoff/handoff.md`](.handoff/handoff.md) §2.2，
  前置 = config 先加 `[models."<id>"]` 档案）；同批用户裁定「slash 交互迁就
  无头模式」= 反模式 → 立项 T7.7（登记 = R12 + tui-slash-ux §13 +
  implementation-plan T7.7）；**`.handoff/` 自本日起只保留
  [handoff.md](.handoff/handoff.md) 一件**（历史批次卡已整合在其 §3）。
  上一阶段：
  **2026-10-08：原型专题文档补充批（rpiv-mono + pi-dynamic-workflow
  两族深读 + dynamic-workflow 形态设计；wiki 侧无代码改动）**——参照从 2 家
  扩到 4 家（新增 rpiv-workflow 声明式引擎 + Pi 社区 JS-in-vm 三仓），
  四家收敛点（7 项四家齐备 + 3 项三家）与互斥登记定稿于
  [dynamic-workflow-shape.md](wiki/port/dynamic-workflow-shape.md)，
  A 表新增专行；**载体路线与排期待拍板**。同日追加**优化池 O1**
  （用户点名：状态行统计口径对齐 grok 默认配置 + 统计状态落会话作单一
  真相 + 计入 subagent 开销）登记于 implementation-plan 优化池 + 对
  S3/S4 的接口影响 = [tui-slash-ux.md](wiki/port/tui-slash-ux.md) §12。
  再上一阶段照旧未收口：
  **2026-10-07 深夜 4：T7.3 交互式改造 + 实测修复批（未提交）**——
  交互式 v1 之上，用户真机验收三条点名（"model 来源应取配置里的 model
  id"；"skills 应显示 skill name 加一部分描述"；"mcp 需要 server 和
  tool 级别都能查看、启用禁用，对标 grok mcps"）落地修复批：
  config `[models."<id>"]` 档案面（下拉 face of record + 启动解析 +
  按档案重建 provider/凭据/窗口/effort，preset/fetch 降级兜底）+
  /skills 行型截断 + /mcp 两级（Status.Tools / MCPToolRows /
  ToggleMCPTool / Enter 展开收起 / Space 启停）。定稿与偏差 =
  [tui-slash-ux.md](wiki/port/tui-slash-ux.md) §11。**工作区累计四批
  未提交**：①thinking 纠偏（§10.9）+ S1 /model + S8 /sessions（§8）；
  ②S2 /rename + 终端标题（§9）；③交互式改造（§10）；④修复批（§11）。
  **用户上轮实测跑的是旧二进制**（本地 pigo.exe 已重建深夜 4，用户常
  用二进制位置待确认）。上批全貌 = 归档
  [handoff-archive.md](wiki/port/handoff-archive.md) 2026-10-07 条。
  （交互式 v1 全貌与拍板项 = 同文件 §10；**遗留缺陷：FileConfigPath 走
  XDG 不认 PIGO_HOME**——LoadSkills 过滤读宿主 config，config 路径统一
  待办。）
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
  ⑤**T7.3 slash 小功能批：S1/S8/S2/交互式改造/修复批 均已落地（交互式 =
  深夜 3"提交命令 → 交互面板"范式；修复批 = 深夜 4，model 档案来源/
  skills 行型/mcp 两级），**已按三段提交（`3509800` / `0936716` / docs 段），
  真机验收待用户**——
  其余切片（S3/S4 /status 增强〔含 /session tab〕、S5 /context
  增强、S7 /recap、S6 /memory 面板、S9 MRU）与 T7.4 主题系统排期待拍板，定稿 =
  [tui-slash-ux.md](wiki/port/tui-slash-ux.md)；⑥T7.5 grok 实用功能移植批已立项（全量普查 =
  [grok-command-inventory.md](wiki/port/grok-command-inventory.md)，
  P1 清单待排期）；⑦farewell 彩蛋已落地（🐼 Code together, cola
  together 退出行）+ header 右段让位滚动条列已修（42 包 0 FAIL）；
  ⑧**T7.6 审批模式三态批已立项（plan / ask / 全部允许的显示 + 切换
  途径，用户点名现状缺失）**——对接 T5.2 trusted/ask seam，**强依赖
  D-C1 本地审批面板**，建议合并设计，登记 =
  [implementation-plan.md](wiki/port/implementation-plan.md) 第 7 期；
  ⑨**dynamic-workflow 编排器形态设计已出（2026-10-08 待议推进）**——
  四参照（grok Rhai 脚本 / zcode TS 脚本 + 编译器 / rpiv 声明式图 /
  pi 族 JS-in-vm）收敛 10 项机制（7 项四家齐备 + 3 项三家），Go 侧三候选与最小内核五切片见
  [dynamic-workflow-shape.md](wiki/port/dynamic-workflow-shape.md)，
  **载体路线（建议候选 C）与排期待拍板**；本件与 code mode 共用脚本
  引擎选型，落点建议新叶子包 `internal/workflow` + 复用
  `runtime.SubAgentSpec` 派发本体；
  ⑩**T7.7 多面命令契约重构已立项（2026-10-08 用户裁定「slash 交互迁就无头
  模式」= 反模式，"交互设计 grok build proxy 这边明显更正确"）**——
  **规格定稿 = [slash-command-surface.md](wiki/port/slash-command-surface.md)**：
  目标形态 = **单份声明目录**（身份/别名/用法/参数形态/来源/适用性谓词/受众门
  全声明，目录序即菜单序）+ **纯 `Parse` → 类型化 `Intent`** + 执行层
  `Execute(loop, Intent)` + **TUI/REPL/headless 三投影器**；删 15 个假注册、
  收编 TUI 17 条拦截清单、headless 对需交互命令明确报错；**幻影命令回归测试已钉
  （2026-10-08 深夜，10 个幻影 + 被拦截 5 名可达性，`phantom_slash_test.go`）**；
  **切片 1 已落地（2026-10-09 `35edcd2`，幻影 10→9，见「当前阶段」）**；
  四切片与验收见该文 §5/§6。登记 =
  [design-principles.md](wiki/port/design-principles.md) **R12** +
  [tui-slash-ux.md](wiki/port/tui-slash-ux.md) §13 +
  [implementation-plan.md](wiki/port/implementation-plan.md) T7.7。
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
  （R1–R12，R11=多参照实现择优：能融合则融合、不能融合取相对最优并在规格
  登记互斥原因；R12=交互形态是命令的第一身份、非交互面只做降级投影；附录已
  登记 7 个 Windows 测试差异族 + `internal/testgate`
  门控机制，
  本机全量测试基线 = 门控后全绿）。**TempDir 清理假报警已断根（2026-10-07；
  2026-10-08 提交 `3509800`）**：`internal/testenv`（`testenv.Main`/`testenv.Dir(t)`）
  推广 session 私有根模式，31 包新增 TestMain、335 处 `t.TempDir()` 替换，清理
  best-effort——新测试一律用 `testenv.Dir(t)` 而非 `t.TempDir()`。
- **T1.1 规格**：[wiki/port/startup-exit-probes.md](wiki/port/startup-exit-probes.md)
  （span 名以 §2.2 埋点表为唯一权威，已含实现偏差登记）。

## 环境事实

- `wiki` 是指向 `D:\wiki\pandawiki\projects\pgo-fork\wiki` 的符号链接，已在
  `.gitignore`（`/wiki`）——**wiki 内容不进 git**，改 wiki 即改 pandawiki 侧文件。
- Windows 开发机：`go build` 间歇被卡巴斯基锁 `%TEMP%`，构建统一加
  `GOTMPDIR=<repo>/.gtmp`（R9）——**只用于 go build**：go test 不受卡巴
  斯基影响，加了反而让 `t.TempDir()` 走 GOTMPDIR 产出混合斜杠路径打破
  trust 测试；全量测试后台跑、输出落文件（R6）。**`GOTMPDIR` 必须传原生
  Windows 路径形态**（`GOTMPDIR='D:\CODE\ai\pgo-fork\.gtmp'`）；传
  `/d/CODE/...` 这种 MSYS 形态会被 Go 拒绝（`creating work dir:
  GetFileAttributesEx ...: The system cannot find the path specified`）。
- dev 分支本地开发，**2026-10-09 起按用户指示推送 `origin/dev`**（daidaiJ/pgo-fork
  fork 仓；上游 PR / 同步策略仍待拍板）。
