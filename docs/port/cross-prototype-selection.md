# 五原型交叉选型：qwen-code / minimax-code / kimi-code / step-code / ZCode（+ crush / grok-build-proxy 参照）

> 状态：选型定稿（2026-10-04，dev 分支）。本文回答一个问题：五个原型 + 两个
> 实现参照里，哪些设计特性适合移植到 pigo 基底。评估维度：**成本、稳定、易用、
> 性能、体验、实用**；工具面与界面交互入口分开标注。
> 已立项的八个专题（[README.md](README.md) 选型表）不重复展开，本文把它们放进
> 统一坐标系并新增候选；施工纪律见 [design-principles.md](design-principles.md)。

## 来源与可信度

| 原型 | 定位 | 与 pigo 的关系 | 可信度 |
|---|---|---|---|
| minimax-code | MiniMax 官方 CLI（TS） | **同源下游**：vendor pi-mono v0.79.1 + 补丁台账 `MINIMAX_CHANGES.md`——它的 agent-modules 几乎是"pi 官方未做增强的参考实现" | A⁻（源码深读） |
| qwen-code | Qwen Code（TS，gemini-cli fork） | 异源；自研增强与 gemini 遗产分界清晰（license header 可辨） | A⁻ |
| ZCode | Z.ai 官方 agent harness（TS monorepo，github.com/zai-org/ZCode） | 异源；设计与 pigo 同题异构 | A⁻（源码 + 本机产品行为佐证） |
| kimi-code | Moonshot Kimi Code CLI（TS） | 异源；grok-build-proxy 有深读底稿（`docs-local/kimicode-port/`） | B⁺（转述底稿） |
| step-code | StepFun Step Code CLI（TS） | 异源；同上底稿（`docs-local/step-code-port/survey.md`） | B⁺ |
| crush | charmbracelet/crush（Go） | 同栈参照（bubbletea v2 + lipgloss v2 + glamour） | A（源码已 clone） |
| grok-build-proxy | xAI grok fork（Rust） | 设计供给方（已立项文档的来源） | A（已发版实现） |

**判定原则**：同题多参照（≥2 个原型独立收敛到同一设计）置信度最高，优先立项；
单一参照且与其宿主架构强耦合的降级为观察。

## A. 稳定性 / 韧性（维度：稳定↑ 性能→）

| 候选 | 参照（多参照加粗） | 要点与 pigo 落点 | 判定 |
|---|---|---|---|
| 防打转哨兵 | **qwen loop 检测 + minimax runaway-guard**（独立收敛，ZCode turn 状态机旁证） | qwen：工具调用 repeat-key（name+args 哈希）连续熔断 + 结果 sha256 内容指纹防"指纹相同内容在变"的轮询误报；minimax：纯函数 turn 级 step-view 指纹，重复 streak ≥3 注入提醒而非硬停。pigo 落点：新叶子包 `internal/runaway`，挂 `runtime` turn seam（`prepareNextTurn`），注入 system-reminder 而非终止 | **P1** |
| 动态 maxTokens + 压缩触发线 | minimax（单参照，但公式通用） | 每轮输出上限 = `min(配置, window - 估算上下文 - margin)` 带输出下限保护；压缩触发线"模型专属分层 + 通用公式"（`computeCompactionTriggerAt`）。落点 `internal/compaction` / provider 预算层 | P1 |
| 会话写租约 + 原子写 | qwen（单参照，JSONL 完整性直接对症） | session-writer-lease 防多进程写坏 JSONL + 全库统一原子写。pigo 会话是单文件 JSONL 树，多进程（task 子代理 + 主进程）并发写是现实场景。落点 `internal/session` | P1 |
| 截断协议 | qwen（单参照） | >2000 字符预览 + 全量落盘文件 + sha256 摘要标签 + 幂等 sentinel 防重复截断。与既有 `clipToolResultContent` 预算层互补（那是字节预算，这是落盘协议） | P2 |
| MCP 连接池 | **minimax + qwen**（独立收敛） | 每服务器持久连接懒启动 + idle 回收 + 信号量限并发（防 LLM 洪泛 fork 炸 stdio 子进程）+ stderr 安全阀 + 指数重试 + 发现超时。落点 pigo MCP 层 | P2 |
| 流恢复注入 | grok（已立项） | 见 [stream-recovery-injection.md](stream-recovery-injection.md) | 已立项 P0 |
| turn 显式状态机 | ZCode（单参照） | `TurnPhase` 枚举 + `canTransitionTo` 合法迁移表，非法迁移显式报错。pigo loop 是隐式状态，重构成本高、收益中期——观察，等 runaway/恢复类特性落地后评估 | 观察 |

## B. 成本 / 上下文治理（维度：成本↑ 性能↑）

| 候选 | 参照 | 要点与 pigo 落点 | 判定 |
|---|---|---|---|
| 微压缩（旧 tool result 原位清理） | **qwen microcompaction + minimax context-manager + ZCode microcompact**（三参照独立收敛，置信度最高） | 不重排历史：老 tool result 原位替换占位符，按 callId 反查 file_path 联动失效读缓存；只压可再生物（Read/Bash/Grep/WebFetch），保留最近 N 条，最小节省阈值。与 pigo 已立项的 canonical context edit **互补不重叠**（微压缩=自动/可再生内容；context edit=显式/任意条目）。落点 `internal/compaction` 中间层 | **P1** |
| cache 前缀稳定纪律 | **qwen + minimax + ZCode**（三参照） | fork/子代理工具声明面保持不变护 cache 前缀；goal 续跑只追加短 hint；描述变更才追加。pigo 落点：写进 deferred-tool-exposure 设计（已含）+ goal 面补充 | **P1**（纪律项） |
| 延迟工具声明 | qwen tool-search + minimax mcp-disclosure（与 pi 原生三参照） | 见 [deferred-tool-exposure.md](deferred-tool-exposure.md)；minimax 的"BM25（中文）+ 调用一次性授权引用"可作二期增强 | 已立项 P1 |
| canonical context edit | grok（已立项，语义源 pi） | 见 [canonical-context-edit.md](canonical-context-edit.md) | 已立项 P1 |
| token 计量落盘 | qwen tokenUsageService | day/month JSONL + 可导出，`/stats` 展示缓存命中率。pigo 有 usage 面雏形，低成本补齐 | P2 |
| goal 预算三窗口 + verifier 证据规则 | qwen（pigo 已有 goal 工具，属增强） | token/turn/wall-clock 三窗口预算；"打印的文本只证明打印了文本"——完成判定必须有对应 tool result 佐证，独立 verifier 只凭 transcript 证据。落点 `internal/agenttool/goal_tool.go` | P2 |

## C. 安全 / 权限（维度：稳定↑ 实用↑）

| 候选 | 参照 | 要点与 pigo 落点 | 判定 |
|---|---|---|---|
| bash 静态安全分析 | grok（已立项，语义源 step） | 见 [shell-command-analysis.md](shell-command-analysis.md)；ZCode 的 bash 只读判定（argv 逐类白名单，约 15 文件）可补充规则表素材 | 已立项 P0 |
| 权限双层注册表 + 硬阻断 | minimax（单参照，语义自洽） | `HARD_BLOCKED_REGISTRY`（灾难性/渗出→终局 deny，**bypass 模式也不放行**）与 `SOFT_RISK_REGISTRY`（强制走判断）分层；决策可携带 `ProposedRule`（"本次允许顺手生成持久规则"）。qwen 的"自改面保护"（对自身配置文件强制走分类器，含 symlink 穿透检查）是同族设计。落点 `internal/cli/run/toolpolicy.go` 扩展 | P2 |
| 工具副作用声明契约 | ZCode（单参照，架构级） | 每工具声明 只读/破坏性/并发安全/最大输出/超时/副作用范围(none\|workspace\|git\|network\|system)，权限系统读声明而非调用点猜测。pigo `AgentTool` 接口扩展字段，**宜早不宜迟**（越晚接，存量工具越多） | P1 |
| PermissionBroker 端口 + hook 信任审批 | ZCode（单参照） | 权限是端口（`PermissionBrokerPort` + Manual/Deny 兜底）而非内嵌逻辑；工作区 hook 默认不可信需显式信任。pigo hooks 已有 9 事件点，补信任模型 | P2 |
| 敏感文件 rg deny-list | minimax（单参照，含坑位注释） | grep/glob/read 共享单一 deny-list，`**/<dir>/**` 锚定任意深度（`.ssh/*` 只挡根目录是已知坑）；定位"纵深防御而非安全边界"。落点读工具族共享层 | P2 |
| Read→Edit 行号自愈 | minimax（单参照，小巧） | read 输出带 `N→` 行号前缀，模型照抄进 edit.oldText 失败时，仅当全部非空行都是前缀形状才自动重试一次。独立小件，≤0.5 人天 | P2（搭车） |
| Skill 扫描信任边界 | ZCode（单参照） | plugin-scope 扫描不跟随 symlink（防 junction 逃逸），path/sourcePath 分离；只吞 ENOENT，EACCES 上抛。pigo pkgmgr/skills 面适用 | P2 |

## D. 工具面（维度：实用↑ 易用↑）

| 候选 | 参照 | 要点与 pigo 落点 | 判定 |
|---|---|---|---|
| 子代理结果信封 | kimi（已立项） | 见 [subagent-result-envelope.md](subagent-result-envelope.md) | 已立项 P2 |
| fork 型子代理（继承上下文） | qwen（单参照） | `subagent_type: fork` 继承父会话完整/最近 N 轮上下文后台跑；`fork_tools` 白名单**保留完整工具声明护 cache 前缀**，违规调用调度前拒绝。pigo task 子代理加 fork 语义是增量 | P2 |
| 结构化提问（questionnaire） | **qwen ask_user_question + minimax questionnaire + ZCode question-panel**（三参照） | 多步问卷/单问题、选项带 description/图片、recommended 安全默认、强制显式回答；TUI 渲染走统一确认色。grok 的 QuestionView 是第四参照（`docs/port` 未立项，纯 UI 状态机设计干净）。落点：新工具 + TUI 问题面板 | **P1**（体验缺口大，弱模型受益明显） |
| 任务自验证协议（VERDICT） | minimax（单参照，极小） | 模型输出单行 `VERDICT: pass/needs_changes/failed`，解析器先计数再校验，多行/畸形视为歧义。与信封同族，搭车实现 | P2（搭车） |
| 后台任务统一底座 | minimax + ZCode（双参照） | bash 后台/子代理/cron 同一 TaskStore：生命周期事件、输出分页读取、按 kind 注册 runner、迟到完成 fencing。pigo 三者散装，统一是中期收益 | P2 |
| worktree 工具化 | qwen（单参照） | `enter/exit_worktree` 让 agent 自主隔离实验，pin/cleanup 生命周期。依赖 git 场景，实用面看用户群 | 观察 |
| read 家族扩展（PDF/notebook） | minimax | read_pdf/read_notebook 结构化读取。场景驱动，等需求 | 观察 |

## E. 体验 / 界面交互入口（维度：体验↑ 易用↑）

| 候选 | 参照 | 要点与 pigo 落点 | 判定 |
|---|---|---|---|
| thinking 展示 + 流式 markdown | crush（已立项） | 见 [tui-crush-components.md](tui-crush-components.md) | 已立项 P0 |
| 结构化提问 UI | 三参照（同 D 表 questionnaire 行） | TUI 问题面板是交互入口的另一半，与工具同期落地 | P1（随 D） |
| BTW 侧会话 | **minimax + qwen**（双参照） | 主会话运行中开 peek 分支问旁支问题；`peek_` purpose 前缀不进 /resume 列表，不改 schema。pigo 的 JSONL 树 + purpose 字段天然契合，成本低体验独特 | **P1** |
| 工具调用摘要行 | qwen（单参照） | 并行工具批次完成后用 fast model 生成 git-subject 风格标签替换 "Tool × N"，Ctrl+O 展开全文。bubbletea 下易实现 | P2 |
| 状态药丸（Goal/Cron pill） | qwen + ZCode | footer 状态药丸显示活动 goal/定时任务。小件 | P2 |
| 可配置键位 | qwen（单参照，数据驱动） | `Command` 枚举 + KeyBinding（key/sequence + 修饰键三态语义）。等用户诉求 | 观察 |
| @文件提及 / 键盘建议 | ZCode + qwen | 输入面 file-mention 补全面。等 TUI 组件化改造（⑤）完成后再排 | 观察 |
| speculative followup（投机执行） | qwen（单参照，杀手级但重） | followup 建议出现即用 forked chat + **copy-on-write 文件 overlay** 后台执行，确认落盘、放弃清理。需要 Go 侧 overlay fs，工程量大——体验跃迁候选，单独立项 | 观察 |
| 子代理侧栏视图 | ZCode + grok（双参照） | 后台子代理独立侧栏 transcript + 工具事件镜像。pigo 有 subagentpanel 雏形，对齐增强 | P2 |

## F. 工程制度（维度：稳定↑ 实用↑）

| 候选 | 参照 | 要点 | 判定 |
|---|---|---|---|
| 补丁台账制度 | minimax `MINIMAX_CHANGES.md` + grok `PATCHES.md`（双参照） | vendor/上游同步的每条差异：reason/影响面/验证方式/上游 PR 状态。pigo 对 pi 上游同步面临同题——fork 改动逐条登记，同步后重放 | **P1**（制度，随第一个改核心包的特性生效） |
| 编译/测试耗时纪律 | grok 反例（用户点名） | R1–R10 见 [design-principles.md](design-principles.md)，含"测小特性不烧全 lib"的 R10 | 已定稿 |
| 启动/退出探针 | grok span 画像层（已立项） | 见 [startup-exit-probes.md](startup-exit-probes.md) | 已立项 P0 |
| 交接台账 | grok port-roadmap §5/§6（单参照） | 跨会话施工的只追加台账 + 中断点登记。已吸收进 [README.md](README.md) 交接纪律节 | 已吸收 |

## 明确不做（共议留档）

- **云绑定全家**：MiniMax OAuth/check-in/云分类员/云工具、qwen DashScope 绑定、
  ZCode 云 marketplace/remote workspace、grok 登录/遥测/remote daemon。
- **宿主形态绑定**：ZCode Electron/RPC/Web/桌面端、qwen IM 渠道网关
  （9 平台适配器）、多宿主分发（IDE 插件/chrome-extension/mobile-mcp）。
- **重架构引入**：QuickJS 第二脚本运行时（code-mode，qwen/grok/pi 三方一致拒绝；
  Go 侧 goja 起步也等真实编排需求）、ultraviolet 整帧渲染、dynamic-workflow 编排器。
- **gemini-cli 遗产类型模型**：学 qwen 的行为设计，不搬 `@google/genai` Part/Content
  渗透层。
- **浏览器全家桶 / 语音 / 视频输入**：无对应能力位与场景。

## 与已立项文档的映射

| 已立项 | 本文对应 | 增量输入 |
|---|---|---|
| rewind-branch-undo | — | step 分支摘要降级为后续评估 |
| shell-command-analysis | C 表第 1 行 | ZCode bash 只读判定补规则素材 |
| canonical-context-edit | B 表 | 与微压缩划界（显式 vs 自动） |
| deferred-tool-exposure | B 表第 3 行 | minimax 中文 BM25/授权引用作二期 |
| stream-recovery-injection | A 表 | — |
| subagent-result-envelope | D 表 | — |
| tui-crush-components | E 表 | — |
| startup-exit-probes | F 表 | — |

## 新立项建议（施工顺序，接 README 施工顺序之后）

**P1**：微压缩 → 防打转哨兵 → 工具副作用声明契约 → questionnaire 提问 →
BTW 侧会话 → 动态 maxTokens → 会话写租约 → 补丁台账（制度）→ cache 纪律回填。
**P2**：C/D/E 表标注 P2 各项。**观察项**不排期，出现场景再立项。
