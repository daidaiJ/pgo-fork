# grok-build-proxy 设计移植总览

> 状态：立项（2026-10-04，dev 分支）。本文是移植施工顺序与"已有判定"的索引；
> 各特性设计细节见分文档。来源项目：`D:\CODE\ai\grok-build-proxy`（xAI grok CLI
> 的社区 fork，Rust，v1.0.42），其设计多数源自对 pi / kimi-code / Step-Code 的
> 预研并已在 v1.0.41/42 实际落地验证——本移植方向相反（fork → pigo），语义
> 参照仍有效，落点重新锚定到 pigo 的 Go 架构。

## 移植原则

1. **剥离 grok 绑定**：grok-build-proxy 的可移植资产集中在
   `session/`、`pager/{scrollback,views,app}`、`acp-lib`、`chat-state` 四处，
   与 xAI 强绑定的部分（OAuth 登录、grok 模型目录、x.ai 端点预设、GROK_* 环境变量、
   会话 token 下发 fallback、遥测三件套、remote daemon、品牌协议）全部不移植。
2. **以 pigo 现有架构为准**：新能力优先做成"新工具 + 新 seam"（`run.SetupEnv`
   汇流、`runtime.RunConfig` seam 消费），不改 loop 主干；会话数据走
   `internal/session` Store 的 Version 递增迁移。
3. **国模适配约束照单全收**：来源项目为国产模型做的适配（capability 门、
   fail-closed 安全下限、prompt cache 友好）对 pigo 的国模 provider 面
   （deepseek/moonshotai/zai/volcengine/dashscope 等）同样成立，逐条进验收。
4. **尊重上游 pi 语义**：pigo 是 pi 的 Go 移植；grok 侧与 pi 相反的设计决策
   （如 context edit 不走 compaction 通道）在分文档里逐条标注取舍。

## 选型结论与已有判定

| 序 | 特性 | 分文档 | pigo 现状（已核实锚点） | 真实缺口 | 优先级 |
|---|---|---|---|---|---|
| ① | rewind 分支树 undo | [rewind-branch-undo.md](rewind-branch-undo.md) | AppendBranch 树存储已有（`internal/session/session.go:637`，REPL/headless/TUI 全走此路径）；`/rewind` 已有（`internal/cli/repl/rewind.go`，文件快照回滚 + 换叶子）；`/tree` 已有叶子导航 | 弃分支点在选择器不可见、prompt 不放回编辑器、compaction 边界不预计算、TUI 无入口 | P0 |
| ② | bash 命令静态安全分析（三态） | [shell-command-analysis.md](shell-command-analysis.md) | toolpolicy 只做工具级准入（`internal/cli/run/toolpolicy.go`），无命令内容分析；工具重试有分类但无安全三态 | AST/词法分析全套 + `analysisIncomplete` fail-closed 语义 + headless 非交互收口 | P0 |
| ③ | canonical context edit | [canonical-context-edit.md](canonical-context-edit.md) | compaction 是重写历史路线（`internal/compaction`）；会话条目无可见面编辑维度 | journal 追加式 ContextEditEntry + prompt 构建层应用 | P1 |
| ④ | 延迟工具声明（三档 exposure） | [deferred-tool-exposure.md](deferred-tool-exposure.md) | 准入控制只有 allow/deny 两态（`toolpolicy.go`），声明面全量；无 deferred/search_tools | 三档 exposure + 按名认领 + 公告流 + 模型能力门 | P1 |
| 随手 | 不完整流恢复注入 | [stream-recovery-injection.md](stream-recovery-injection.md) | 传输层有连接期重试（429/503/529），工具失败有重试；无"流中断→下一轮续作指令注入" | projection-only 注入一条恢复提示 | P0（搭车） |
| 随手 | 子 agent 结果信封 + next_step | [subagent-result-envelope.md](subagent-result-envelope.md) | task 子代理已有（进程隔离 JSON-RPC + 进度事件）；无结构化信封/stop_reason 映射 | 信封字段 + stop_reason→next_step 映射 + 失败可 resume | P2 |
| ⑤ | TUI 组件改造（参考 crush） | [tui-crush-components.md](tui-crush-components.md) | Bubble Tea v2 + glamour（回合结束渲染）；`ThinkingContent` 已解析进消息（`internal/agentcore/content.go:42`、`internal/provider/openai.go:69`）但 TUI/REPL 均不渲染 | thinking 块展示、流式 markdown、组件化与终端兼容性 | P0 |

## 施工顺序

**随手-流恢复注入 → ② → ⑤（TUI thinking/md，独立线可并行）→ ① → ③ → ④**
（④ 开工前需先定 pigo 侧模型能力位载体）。

理由：随手件独立便宜且对国模网关流中断直接对症；②是无人值守安全基座；
⑤与①③④无代码耦合、可并行推进；①依赖的会话树机制已有，改的是选择器与
编辑器回填；③④动声明面与 prompt 构建，按来源项目经验放最后（风险最高）。

## 来源与可信度约定

- **来源项目已验证实现**：grok-build-proxy v1.0.41/42（2026-10-03/04 发版，
  四特性 + 流恢复注入已合 main 并过 Linux CI），Rust 锚点在各分文档"来源"节。
- **pigo 侧现状**：A 级（本机 grep/读码，锚点随文）；来源语义：A 级（已发版实现）；
  收益/人天估算：C 级粗估。
- 原始设计文档在来源项目 `docs-local/`（`kimicode-port/rewind-branch-undo.md`、
  `step-code-port/survey.md`、`workflow-pi-port/{canonical-context-edit,deferred-tool-exposure}.md`、
  `port-roadmap.md`）——本文档集是它们面向 pigo 的调整版，不重复原文全文。

## 交接纪律（承自来源项目 port-roadmap §5）

每个特性开工前先复核本 README 的"已有判定"列（防把 pigo 已有设计当缺口）；
每片绿一块提交一块（测试 + `go vet` 过了才 commit）；完工回填本文状态列；
中途中断时接手会话先 `git log` + `git status` + diff 核对实际进度再续作，
禁止凭记忆续写。
