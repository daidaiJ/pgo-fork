# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## 🔄 Handoff 摘要

### T8.2 LSP 批 2（B1/B3/B4 落地 + B2 取证改判不落地）— 已落地并推送

- **当前状态：** 2026-10-11 晚落地（接手首件：按交接卡排期口径取待排期池
  首位；feat + docs 两段提交推送 origin/dev）；44 包 0 FAIL
  （`.gtmp/t82b2-final.log`）；两份二进制已刷（第二份 = cp，sha256 一致）。
  **B2（watched_files 代理）取证改判不落地（D-12）；B5（rename + 多路由）
  缓入随 T7.6+D-C1**；余项全显式登记（lsp-support.md §7）。第 8 期维持
  ✅ 全部完成 4/4。
- **关键证据：** ①**B1 评估 = 维持独立工具族（5→7 件）**——deferred 面
  pre-claim 零成本 + 认领粒度细，grok 单工具多操作是其「内置全直出」经济学
  产物；真机 gopls 宣告 workspaceSymbol/implementation/callHierarchy 全
  available，call hierarchy 缓。②**新查询面 `lsp_workspace_symbols` +
  `lsp_implementations`**（真机：`agentLoop` 100 命中首条精确 loop.go:175、
  `AgentTool.Name()` 45 实现者）。③**per-tool 开关**：面板工具行 Space 翻转
  写全局 `[lsp.gopls] tools`（`config.SetLSPTools` 注释保留；下会话生效）+
  REPL `/lsp tool enable|disable <name>`（`IntentLSPToolToggle`；未知名带
  全家清单；**禁到最后一件拒绝**）。④**overlay 面扩 go.mod/go.work**
  （languageId 映射；批 1 的 filter 会静默丢 modfile 写盘——补齐）。⑤**B3
  拉取兜底**（grok pull.rs 规则：MethodNotFound 才写死；对 gopls 一次写死
  零成本）+ **restart 收割/重放**（overlay docs 内存收割 → 重建重推，优于
  grok 读盘重放）+ 启动失败 Status 可见（lastStartErr）。⑥**B4 自动安装**
  （LookPath 失败 → `go install @latest` 到 `<UserCacheDir>/pigo/bin`；
  `[lsp.gopls] auto_install` nil 默认 true；custom command 永不装；D-14）。
  ⑦**B2 改判**：grok 刻意不喂 bash（注释原文取证）+ gopls 宣告后把
  `**/*.{mod,work}` 委托客户端 ⇒ 宣告 = bash 驱动 go.mod 变更回归；替代 =
  overlay mod/work 扩面。
- **拍板登记（本轮默认拍板，可推翻）：** 工具族维持多工具 / per-tool 开关写
  全局层 / auto_install 默认 true / call hierarchy 缓 / 主动 restart 监视
  不做（D-13，pigo 无空闲期诊断消费方）。
- **详情指针：** 规格 + 批 2 定稿 + 偏差 D-12/13/14 =
  [wiki/port/lsp-support.md](wiki/port/lsp-support.md)（§7 本轮权威）；
  任务 = implementation-plan T8.2 + [plan/phase-8.md](wiki/port/plan/phase-8.md)
  （期维持 ✅ 4/4）；卡 = [wiki/port/handoff.md](wiki/port/handoff.md)
  2026-10-11（晚）。

### T8.3 输入排队机制补全（队列 pane + 中断冻结 + send-now 插队）— 已落地并推送

- **当前状态：** 2026-10-11 落地（feat + docs 两段提交后推送 origin/dev）；
  44 包 0 FAIL（`.gtmp/t83-fulltest.log`）；两份二进制已刷（第二份 = cp，
  sha256 一致）。拍板四条全按最小建议当日落地；**第 8 期改判 ✅ 全部完成
  （4/4，T8.2 批 2 为任务内余项不降级）**。队列 pane 无终端不可自验，真机
  手感待用户（与 /lsp、/shell 面板同批约）。
- **关键证据：** ①`internal/cli/tui/queuepane.go`（新）：`#N` + 首个非空行 +
  灰 `( +N lines)` 后缀（截断保后缀，grok QueuePane 同构）、cap 3 行窗口、
  ↑/↓ 选中（subagent 面板同手势：首 ↓ 落顶行 / 首 ↑ 落末行）+ Del 删除
  （无选中 no-op）+ `popQueueFront`/`startQueued`；②`model.go`：`sendNow`/
  `queueHeld`/`qpane` 字段 + runEndMsg 排空重写（**held 不排空** + send-now
  先于 queued + **slash 项内联执行不卡死空闲队列**）+ interruptOrQuit 置
  held、退出清双队列 + startPrompt 解冻单点 + handleKey 队列键分支（composer
  空才有键，subagent 面板保持优先）+ `alt+enter` 分支 + 渲染双态（running 槽
  spinner 行上 / held 态输入框上方，relayout 同步预留）+ keyBinds 四态；
  ③`input.go`：InsertNewline 收窄为 shift+enter/ctrl+j——**Alt+Enter 转作
  send-now**（无 Kitty 协议终端换行回退剩 Ctrl+J）；④**顺手修复两件**：入队/
  插队先展开 paste/image 占位符（原样直存会带占位符字面量开跑）；slash 项
  排空卡死。tui 包新增 7 钉（插队次序/冻结不偷跑/手动提交解冻/pane 行型+
  删除/held 可见+退出清队/slash 不卡队/占位符展开）。
- **拍板四条（2026-10-11 用户确认）**：①Esc 中断后队列冻结不偷跑（裸 Enter =
  提升队首，新提交 = 先跑新 prompt 再恢复排空）；②send-now = Alt+Enter 入首批
  （已排队项顺延保留，grok interjection 同构）；③pane 最小版只可见+可删
  （重排/行内编辑/鼠标按钮缓）；④REPL 维持终端缓冲现状（降级口径）。
- **详情指针：** 规格 + 偏差 = [wiki/port/queue-management.md](wiki/port/queue-management.md)
  （收敛/分叉表 + §5 偏差登记）；任务 = [implementation-plan.md](wiki/port/implementation-plan.md)
  T8.3 + [plan/phase-8.md](wiki/port/plan/phase-8.md)（期 ✅ 全部完成 4/4）；
  卡 = [wiki/port/handoff.md](wiki/port/handoff.md) 2026-10-11。

### T8.2 LSP 支持批 1（internal/lsp 内核 + overlay 编辑回灌 + /lsp + 配置分层）— 已落地并推送

- **当前状态：** 2026-10-10 深夜 2 落地（feat + docs 两段提交后推送
  origin/dev）；43 包 0 FAIL（`.gtmp/t82-final.log`）；两份二进制已刷
  （第二份 = cp，sha256 一致）；**真机 gopls v0.23.0 核验过**：overlay 注入
  编译错误 → publishDiagnostics 3.1s（含冷启动）、definition 命中
  `manager.go:94:19`、symbols 32 顶层；管道真机：headless `-p /lsp` exit 2
  拒绝、REPL 降级 listing 双态、`/lsp enable` trust 门拒绝→信任后写盘。
  本批中段用户两点点名已落：**用途 = 加速检索 + 语法检查**；**对标
  opencode + grok**（六项收敛/八项分叉登记，编辑结果内联诊断本轮补齐）。
- **关键证据：** ①`internal/lsp` 叶子包（Content-Length 帧 JSON-RPC +
  initialize 握手 + overlay docs + publishDiagnostics 收集 + 四查询 +
  UTF-16 换算；gopls 默认 `-remote=auto serve` daemon 复用 + flag-error
  降级重试 + Windows URI）。②overlay 工作流 = edit/write 工具缝注入
  （`InjectLSPOverlay`），写盘即全量推送（版本单调 + mailbox 保序）+
  **编辑结果内联诊断**（error 级 cap 10、1.5s 编辑预算；grok drain 注入 /
  opencode edit.ts 同构）。③5 工具族默认 deferred（与 declaration_mode
  解耦）；`/lsp` 声明命令（ProjLSPPanel，TUI 两级面板照 /mcp；REPL 文本
  listing；headless 拒绝）。④配置分层 = 全局 `[lsp]`（默认 off + idle +
  `[lsp.gopls]` command/args/tools）< 项目层 `./.pigo/config.json`
  `{"lsp":{"enabled":…}}`（**trust 门**，写 fail-closed）< `PIGO_LSP`；
  解析单源 `run.ResolveLSPSettings`。⑤**对标**：grok（多 server 路由 /
  watched_files 代理 / pull / restart 监视 / 500ms drain）vs opencode
  （单工具 9 操作 / 5s 等待 / gopls 自动安装）→ 批 2 B1–B5 登记。
  **本轮默认拍板四条**：全局默认 off / rename 缓入 / 面板启停写项目层 /
  非 Go 仓不预热。
- **详情指针：** 规格 + 对标表 = [wiki/port/lsp-support.md](wiki/port/lsp-support.md)
  （本轮权威）；任务 = [implementation-plan.md](wiki/port/implementation-plan.md)
  T8.2 + [plan/phase-8.md](wiki/port/plan/phase-8.md)（期改 ✅2·⏳1）；
  卡 = [wiki/port/handoff.md](wiki/port/handoff.md) 2026-10-10（深夜 2）。

### T8.4 /shell 切换 + 默认 shell 运行时后端配置 — 已落地并推送

- **当前状态：** 2026-10-11 落地（feat + docs 两段提交后推送 origin/dev）；
  44 包 0 FAIL（`.gtmp/t84-final.log`）；两份二进制已刷（第二份 = cp）；
  管道真机八件核验：REPL listing 双态（auto = bash current / config=pwsh
  current）、热切 + 写盘 `[shell] backend = "pwsh"` + 新进程跨进程生效、
  未知后端解析拒绝、`PIGO_SHELL=cmd` env 层、非法 env 启动报错、headless
  `-p /shell` exit 2。**拍板五条（用户确认）**：级联保持 bash 优先 / 会话内
  热切 / 只允许全局层 / 非 bash 后端跳过 guard（宽松，风险登记）/ 加 `shell`
  别名。**同轮拍板登记**：T8.3 四条全按最小建议、dynamic-workflow 选 C +
  硬路由排实用件后、O1 与 S3/S4 合并、上游维持现状、**D-11 = MCP 默认
  deferred 补齐立项（对标 grok，非本期）**、subagent 派发检查随 T7.1 观察。
- **关键证据：** ①config `[shell]` 表（backend + custom command/args）+
  `PIGO_SHELL` > 探测；解析单源 `run.ResolveShellSettings`（未知名 exit 2）。
  ②`BashTool` + `ShellArgs` 前缀 + `SetShellSpec` 热切缝 + **修显式 Shell 恒
  `-c` 坑**（basename 推断 flag 形态）+ `run.SetBashShell` 三路径注入 +
  `Env.Bash`。③**`shell` 别名**（ShellAliasTool 委托同一实例）+ 边界三处
  能力族归一（toolpolicy deny/allow 全族、shellguard 双名过门、T5.2 规则
  `ruleFamily`）。④`/shell` 声明命令（ProjShellPanel：TUI 面板 Space 切换 /
  REPL listing / headless exit 2）+ `SetShellBackend` 写盘（注释保留、缺失
  拒写同 /skills 口径）；目录钉 29→30。⑤**grok 取证（D-11 答复）**：grok
  只对 MCP defer（常驻 search_tool + use_tool 分派，内置全直出），pigo
  T4.1 三档即其同构物，D-11 补齐 = 对齐此形态。
- **详情指针：** 规格 = [wiki/port/shell-switch.md](wiki/port/shell-switch.md)
  （拍板五条 + 收敛/分叉表 + 边界登记）；任务 = implementation-plan T8.4 +
  [plan/phase-8.md](wiki/port/plan/phase-8.md)（期改 ✅3·⏳1 / 4）；卡 =
  [wiki/port/handoff.md](wiki/port/handoff.md) 2026-10-10（深夜 3）。

### 实施计划分册拆分 + 期级整体状态标记（索引 + plan/ 分册）— done（wiki 侧，无代码改动）

- **当前状态：** 2026-10-10 深夜完成（用户点名三连：按分期拆分 + 状态不清 +
  部分完成无登记 + 索引要能直接判"这期是否全部完成"免检索 + handoff 关联
  勿破坏）。`wiki/port/implementation-plan.md` = **索引**：状态图例含
  **期级判定口径**（✅ 全部完成 = 该期所有任务 ✅，任务内已登记小余项不降级
  期状态；🔶 部分完成 = 存在 🔶/⏳ 任务；⏳ 未开工 = 期无落地件）+
  **状态总览表带「整体状态 / 未完结项（含任务内余项）」两列**（判断这期
  完没完、剩什么免开分册）+ **「与交接工作流的关联」段**（收口回填三件套 =
  分册任务状态 + 索引总览行 + 交接卡；旧引用「implementation-plan T<期>.<序>」
  兼容说明）；任务详录在 **`wiki/port/plan/`**：`phase-1..8.md`（每任务 =
  状态行 + 端/规格/规模 + 交付 + **余项显式登记**，期首「状态一览」表）+
  `backlog.md`（待议/优化池 O1/观察池/负面清单 + 已升级件去向）。**无代码
  改动**；port/README 指针同步；wiki 交接卡已轮换（T8.1 卡入 archive）。
- **对账要点：** 第 1–5 期 ✅ **全部完成**（3/4/5/4/4）；第 6 期 🔶 部分完成
  = **✅8（含 T6.4 盘点件——其内置精选批 2026-10-10 用户拍板废弃）·
  ⏳2（T6.3 usage MVP、T6.6 韧性件——原表无状态标记，本轮补 ⏳ 未开工）**；
  第 7 期 🔶 部分完成 =
  ✅ T7.7/T7.8、🔶 T7.3（四批已落地 / 余 S3–S7·S9 + 真机验收）与 T7.5
  （普查 + farewell ✅ / P1 清单 ⏳）、⏳ T7.1/T7.2（调研定案待实作）/T7.4/
  T7.6；第 8 期 🔶 部分完成 = ✅ T8.1/T8.2/T8.4、⏳ T8.3（四条已拍板待开工）。任务内余项显式化：T6.1 `Repair(id)` 未接线、T6.10 截图回归
  待用户、T7.8 `/dump` TUI 面、T5.1 所有权校验（→ T7.1 衔接）等。
- **详情指针：** 索引 = [wiki/port/implementation-plan.md](wiki/port/implementation-plan.md)
  （整体状态判定 + 未完结项 + 交接关联段）；分册 = `wiki/port/plan/`
  （phase-1..8 + backlog）；交接卡 = [wiki/port/handoff.md](wiki/port/handoff.md)
  （最新卡；旧卡入 handoff-archive.md）+ `.handoff/handoff.md` §1。

### T8.1 供应商配置统一批 — 已落地并推送

- **当前状态：** 2026-10-10 晚落地（feat + docs 指针两段提交，**推送 origin/dev**）；
  43 包 0 FAIL（`.gtmp/t81-fulltest.log`）；两份二进制已刷（第二份 = `cp`）；
  真机核验（隔离 HOME + 假端点）：config 供应商继承链路零 flag 跑通、未知 id /
  no-model 明确拒绝、/models 列 config providers + profiles。
- **关键证据：** ①**config 归一 `~/.pigo`**（双轨缺陷收口）：`FileConfigPath`
  = `$PIGO_HOME/config.toml` 否则 `~/.pigo/config.toml`；`LoadUserConfig`
  权威优先 + legacy XDG 只读回退 + **一次性迁移副本**；`PIGO_HOME` 显式时不回
  读宿主 XDG。②**`[provider."<id>"]` 连接节**（base_url/protocol/api_key/
  credential/env_key/proxy）+ 档案逐字段继承（**凭据整组 opt-out**，grok
  `with_provider_defaults` 对齐）+ `ResolveModelConnection` 单源；坏节跳过 +
  启动 warn，引用未命中 fail-closed；顶层标量 `provider` 与节共享 TOML 键
  （`UnmarshalTOML` 按形态路由）。③**两级代理**：`ProxyClient`（非法 URL
  fail-fast）+ 驱动 `setHTTPClient` 注入缝 + `ResolveProviderWithProxy`
  （模型 > 供应商 > 默认传输；SetupEnv / switchToProfile / executor 裸切换
  接线）。④**解析拍板落地**：openrouter catch-all 与 `--model` 默认
  `"openrouter/free"` 双双退役（未知 id / no-model 显式报错）；显式 base-url =
  `custom` 通用驱动；/models 列 config providers + profiles。
  **参照 = grok-build-proxy `583ac36`（v1.0.44）**：`[model_providers.<id>]`
  引用继承 + fail-closed + LOCAL 二十三/二十四期出口代理（use_proxy opt-in +
  三级自动探测——注册表探测未落，偏差登记）。
- **详情指针：** 规格 = [wiki/port/provider-config.md](wiki/port/provider-config.md)
  （§7 偏差 + 落地补充登记）；任务 = [implementation-plan.md](wiki/port/implementation-plan.md)
  **T8.1**；卡 = [wiki/port/handoff.md](wiki/port/handoff.md) 2026-10-10（晚）。

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

### T7.7 命令面契约重构切片 4 收口 — 已落地（T7.7 四切片全部完成）

- **当前状态：** 切片 4 落地（2026-10-10，feat + docs 指针两段提交，
  **推送 origin/dev**）；**T7.7 收口**（切片 1 = `35edcd2`、切片 2 = `4bb2321`、
  切片 3 = `3a36a02` 照旧）；43 包 0 FAIL（`.gtmp/s4-fulltest2.log`）；两份
  二进制已刷（第二份 = `cp`，sha256 一致）；REPL 别名派发 + headless 别名拒绝
  已用本地假端点（SSE）+ 管道配方真机核验。
- **关键证据：** **别名索引**（`SlashRegistry.aliases` 别名→canonical；别名与
  命令名同一命名空间、同一 tier 规则：低阶后到撞内建别名 = 影子记录、内建互撞
  = panic〔grok `rebuild_triggers` 对齐〕；`Lookup`/`Remove` 别名感知）；
  **别名声明转正**（`think.Aliases=["effect"]`、`sessions.Aliases=["resume"]`，
  两张重复注册删除，目录钉 30→28 + 2 别名）；候选面三处换源 `Candidates()`
  （canonical 行 + 别名行〔`alias of /x:` 描述前缀〕，TUI 菜单 / REPL 补全 //
  /help，打字 `/ef` `/res` 仍可补全；`List()` 保持 canonical-only）；
  **解析路径统一**（`ResolveOutcome` 走 `SplitInvocation`+`Lookup`，Parse/
  Action/Run/Expand 优先级抽 `resolveCommand` 单源；不可用通知与 usage 行保留
  调用形态——打 `/resume` 回 `/resume`）；**受众门 fail-closed**（新
  `ResolveModelAuthored`：仅 `AudienceHumanAndModel` + 精确 canonical〔别名
  刻意绕开〕可解析，其余一律原文返回按纯文本——grok `slash_authority` 对齐；
  **pigo 现无模型自撰解析路径**，语义与测试矩阵先钉，消费面接入零改动）；
  测试 = runtime `slashalias_test.go` 矩阵 + declarations 28+别名钉 + headless
  别名拒绝 + tui 菜单别名行钉。
- **详情指针：** 规格 + 偏差 = [slash-command-surface.md](wiki/port/slash-command-surface.md)
  §5 切片 4 标注 + §7 切片 4 段（切片 1-3 段照旧）；任务 =
  [implementation-plan.md](wiki/port/implementation-plan.md) T7.7 ⑩；
  卡 = [wiki/port/handoff.md](wiki/port/handoff.md) 2026-10-10。

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
- [ ] **T8.3 TUI 队列 pane 真机手感**（`#N` 行型 / ↑↓ 选中 + Del 删除 /
      Esc 中断后冻结不偷跑 + 裸 Enter 提升队首 / Alt+Enter send-now 插队
      ——各终端的 Alt+Enter 序列形态；前置 = 无，纯 TUI 件）
- [ ] **T8.4 TUI /shell 面板真机手感**（后端列表级联序 + `[current]` 标注 +
      Space 切换写全局 config + 下一条命令热切生效；前置 = 无，全局开关件）
- [ ] **T8.2 TUI /lsp 面板真机手感**（server 行状态/诊断计数 + Enter 展开
      工具行 + Space 启停写项目层 + **批 2：工具行 Space 翻转写
      `[lsp.gopls] tools` 的手感**；前置 = `[lsp] enabled = true` 或项目层
      开 + 目录受信）；批 2 已落地（2026-10-11），**余 B5 = lsp_rename 过
      T5.2 + 多 root/多 server 路由（随 T7.6+D-C1 取件）** + call hierarchy
      缓（lsp-support.md §7）
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
- [ ] slash 菜单 [skill] 标记 + /resume 别名 + 菜单别名行（/effect //resume）
      + `/model <id> <effort>` 参数式
- [ ] **T7.8 `/dump` 的 TUI 面**（打成系统块 + 路径可读）；自动落盘与 `/dump`
      已在 REPL/headless 真机核验；**流式开始后的失败不落盘**为登记口径
      （原始响应不可复原），流式失败只照原样上报
- [ ] 上三批：S2 终端标题、S1/S8、thinking 布局、交互式 v1 真机对表 grok
- [ ] **T7.7 遗留（2026-10-10 四切片收口，本件关闭）**：`Suggest`/`Preselect`
      与 `Offered` 谓词缓落（候选是前端投影的 live 数据，偏差第四次登记）；
      9 个 REPL-face 命令的 TUI 面板化 = 后续独立批；受众门生产消费面
      （subagent 输出 / code mode）接入时走 `ResolveModelAuthored`
      （fail-closed 语义已钉 + 测试矩阵）；规格 =
      [slash-command-surface.md](wiki/port/slash-command-surface.md)
      （§7 切片 4 段 + 切片 1-3 段；登记 = R12 + tui-slash-ux §13.5 +
      implementation-plan T7.7）

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**2026-10-11 晚：T8.2 LSP 批 2 落地（B1/B3/B4 + B2 取证
  改判）**——工具族 5→7（评估维持多工具）+ 新查询面 workspace_symbols/
  implementations（真机 100/45 命中）+ per-tool 开关（面板 Space + REPL
  文本式，写全局 `[lsp.gopls] tools`）+ overlay 扩 mod/work + pull 兜底 +
  restart 收割重放 + lastStartErr 可见 + gopls 自动安装（D-14）；B2 改判
  不落地（D-12 取证反转），B5 随 T7.6+D-C1；44 包 0 FAIL
  （`.gtmp/t82b2-final.log`）。规格 = wiki/port/lsp-support.md（§7 本轮
  权威），任务 = implementation-plan T8.2。上一阶段：
  **2026-10-11：T8.3 输入排队机制补全落地（四条拍板当日收口，
  第 8 期 ✅ 全部完成）**——队列 pane（`#N` + 首行 + `( +N lines)`，cap 3，
  ↑/↓ 选中 + Del 删除，running 槽 / held 态双渲染）+ **Esc 中断后队列冻结
  不偷跑**（裸 Enter 提升队首 / 新提交解冻；退出清队）+ **Alt+Enter send-now
  插队**（InsertNewline 相应收窄为 shift+enter/ctrl+j）+ 顺手修复（入队占位
  符展开 / slash 项排空卡死）；44 包 0 FAIL（`.gtmp/t83-fulltest.log`）；规格
  = wiki/port/queue-management.md，任务 = implementation-plan T8.3（拍板四条
  当日收口）。上一阶段：
  **2026-10-10 深夜 3：T8.4 /shell 切换 + 默认 shell 后端配置落地**——config
  `[shell]` 表 + PIGO_SHELL 覆盖链（全局层 only）+ 热切缝
  （修显式 Shell 恒 `-c` 坑）+ `shell` 别名（三处能力族归一）+ /shell 三投影
  + shellguard 非 bash 后端跳过；五条拍板当日收口（规格 =
  wiki/port/shell-switch.md，任务 = implementation-plan T8.4，期改 ✅3·⏳1）。
  同轮拍板：T8.3 四条、dynamic-workflow C + 硬路由、O1 与 S3/S4 合并、
  上游维持现状、**D-11 = MCP 默认 deferred 补齐立项（对标 grok，非本期）**。
  上一阶段：
  **2026-10-10 深夜 2：T8.2 LSP 支持批 1 落地**——internal/lsp 内核 +
  overlay 编辑回灌（编辑结果内联诊断）+ /lsp 面板 + 配置分层 trust 门 +
  5 工具默认 deferred；对标 opencode + grok 收口（规格 =
  wiki/port/lsp-support.md，任务 = implementation-plan T8.2）。
  上一阶段：
  **2026-10-10 晚：T8.1 供应商配置统一批落地**——config 归一
  `~/.pigo`（双轨缺陷收口：legacy XDG 只读回退 + 一次性迁移副本）+
  `[provider."<id>"]` 连接节 + 档案逐字段继承（凭据整组 opt-out，grok 对齐）+
  供应商/模型两级代理（`ProxyClient` fail-fast + `setHTTPClient` 注入缝 +
  `ResolveProviderWithProxy`）+ 解析拍板落地（openrouter catch-all 与
  `--model` 默认退役，未知 id / no-model 显式报错；显式 base-url = `custom`
  驱动）+ /models 列 config providers + profiles；43 包 0 FAIL
  （`.gtmp/t81-fulltest.log`）；参照 = grok-build-proxy `583ac36`（v1.0.44）；
  规格 = wiki/port/provider-config.md，任务 = implementation-plan T8.1。
  上一阶段：
  **2026-10-10：T7.7 切片 4 落地并收口（T7.7 关闭）**——
  **别名解析 fail-closed**（`SlashRegistry` 别名索引：别名与命令名同一命名
  空间同一 tier 规则，内建互撞 panic〔grok 对齐〕；`/effect`→`/think`、
  `/resume`→`/sessions` 转正为 `Aliases` 声明，重复注册删除，目录钉 30→28
  + 2 别名）+ 候选面三处换源 `Candidates()`（canonical 行 + `alias of /x:`
  别名行，TUI 菜单 / REPL 补全 //help，`List()` 保持 canonical-only）+
  解析路径统一（`ResolveOutcome` 走 `SplitInvocation`+`Lookup`，解析体抽
  `resolveCommand` 单源；通知/usage 保留调用形态）+ **受众门 fail-closed**
  （`ResolveModelAuthored`：HumanAndModel + 精确 canonical 双 fail-closed、
  别名忽略，其余按纯文本；pigo 现无模型自撰解析路径，语义+测试先钉）；
  43 包 0 FAIL（`.gtmp/s4-fulltest2.log`）；REPL 别名派发 + headless 别名
  拒绝管道真机核验过；规格 = slash-command-surface.md §5/§7 切片 4 段，
  任务 = implementation-plan T7.7 ⑩。
  上一阶段：
  **2026-10-09：T7.8 请求转储落地（`e55fc51`，已推 origin/dev）**——
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
  tui-grok-style §1 截图，同第 2 期收口流程）；②**T6.4 pstack 内置精选批已废弃（2026-10-10 用户拍板；盘点件留档 =
  [pstack-skill-inventory.md](wiki/port/pstack-skill-inventory.md)）**；
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
- **⑪ T8.2 LSP 支持（gopls 深度适配）已立项（2026-10-10 用户点名，建议排
  T8.1 后下一个实用件）**：`internal/lsp` 客户端内核（**overlay 诊断回灌** =
  核心优化：didOpen/didChange 喂内存态，编辑后诊断即时回灌）+ gopls 深度适配
  （后台预热 / `gopls -remote=auto` daemon 复用 / Windows URI / 多 module）+
  **工作目录级开关走既有项目层 `./.pigo/config.json`（trust 门 = 安全前置）**+
  **`/lsp` 声明命令**（TUI 两级面板照 /mcp 先例，REPL 降级，headless 拒绝）+
  工具族**默认 deferred**（deferred 面 T4.1 机制；+5435 token/轮教训不得重犯）；
  待拍板 = 全局默认 off/on / rename 是否入首批（多文件写须过 T5.2）/ 面板启停
  写哪层 / 非 Go 仓空转防护（登记 = implementation-plan T8.2）。
- **⑫ T8.3 输入排队机制补全（照 grok）已立项（2026-10-10 用户点名「疑似没有
  排队机制」；核查完成）**：**TUI 已有基础队列**（running 中 Enter 入队 +
  runEndMsg 逐条排空，有测试钉）——缺 grok 的队列管理面（不可见/不可管理）
  且**中断语义有坑**（Esc 中断后排队项自动开跑）；REPL 靠终端缓冲巧合。
  grok 取证 = QueuePane（#N 列表）/ 队列编辑 / **send-now 插队** / 取消分级
  （interactive 只取消 running、保留排队；hard teardown 才全清）。建议三片 =
  ①可见+可管理（#N pane）②取消分级（中断后队列冻结不偷跑）③send-now 修饰键
  插队；待拍板四条 = 中断后队列行为 / send-now 键位与首批 / pane 重排 /
  REPL 预缓冲回显（登记 = implementation-plan T8.3）。
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
  （**索引 + 状态总览**；任务详录按期拆分为分册 `plan/phase-1..8.md` +
  `plan/backlog.md`，2026-10-10——状态图例 ✅/🔶/⏳，部分完成件列
  「已完成/余项」清单）。
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
