# P1 设计原型：canonical context edit（不改写历史的可见上下文编辑）

> 来源：grok-build-proxy v1.0.41 已发版实现（`feat/local-workflow-context-edit`，
> 合 main `904dc9a`）。原始设计 `docs-local/workflow-pi-port/canonical-context-edit.md`
> （语义源 pi `ContextEditEntry`，docs/session-format.md）。本文是其面向 pigo 的调整版。

## 1. 问题

长会话/多轮子代理编排中，前序轮次的完整工具结果持续占据上下文：现有治理
手段只有 compaction（`internal/compaction`）——**重写历史**的路线。pigo 的
会话是 append-only JSONL 树（`internal/session`），compaction 一旦改写，
回放/恢复语义就依赖摘要质量，且粒度是"整段对话"而非"某条结果"。

需要的是：**历史条目物理不动，模型每轮收到的是"全量历史 + 编辑条目"应用后的
可见面**。编辑本身是会话历史的合法一等事件（可持久化、可随分支继承）——
pi `ContextEditEntry` 的语义。

## 2. 语义定案（与 pi/来源实现对齐）

- 历史（session JSONL）只增不改；可见面 = 原始条目 + 按 seq 顺序应用的编辑序列。
- 编辑类型两种：`replace_visible(seq, digest)`（把 seq 号条目在后续轮可见面中
  替换为摘要占位）与 `hide_visible(seq)`（从可见面移除，如中间产物工具性输出）。
- 首次生效：同一 seq 的二次编辑忽略（追加式 vs 首次生效取后者，重放实现最简）。
- 占位格式 v1 定为文本行（如 `[result #<seq> summarized: <digest 前 80 字>]`），
  真实场景校准后再定结构化形态。

## 3. 落点（pigo 侧）

1. **会话条目**：`internal/session` Entry 增加编辑类条目（Version 递增 +
   readSession 向后兼容迁移——旧文件无编辑条目即可见面 = 原史，零漂移）。
   编辑条目携带 `seq`（目标 entry 序号）+ 类型 + digest + 原条目哈希（防编辑
   指向被回退掉的分支条目时错配）。
2. **上下文构建层应用**：`Store.Load`/`PathToLeaf` 重建 `MessageList` 后套用
   编辑序列（`internal/session` 内聚，REPL/TUI/headless 三驱动共用）。树分支
   天然继承：编辑条目在其所在分支上生效，与叶子指针语义一致——这是相对
   来源项目（线性 journal 需要 seq + 分支折叠）的又一次简化。
3. **编辑入口**：
   - 内置工具 `context_edit`（`internal/agenttool`，走 `AgentTool` 接口 +
     JSON Schema 校验 + tool policy 准入），模型可在 agent 侧自主精简历史
     （配合 prompt 指引）；参数 = seq 列表 + 模式（replace/hide）+ digest。
   - compaction 联动（P2 可选）：compaction 改走"对被压缩段追加 hide 编辑 +
     注入摘要消息"而非重写，彻底消除重写路线（先登记，等 ① 落地后评估）。
4. **可见性约束**：被 hide/replace 的条目仍参与 `clipToolResultContent` 等
   裁剪管线（编辑发生在裁剪之前）；审计面 `session export`（HTML）始终展示
   原始内容 + 编辑标记。

## 4. grok 绑定剥离

来源实现的 host 通道绑定其 Rhai workflow 引擎（`host_call` journal 直记 +
spawn payload 哈希耦合 + divergence 检测）——pigo 无 Rhai，全部不搬；
pigo 侧编辑入口换成内置工具 + 会话条目，重放确定性由"编辑即会话条目"天然保证
（Load 重放时编辑条目自会再应用一次），不需要 hash 链机制。

## 5. 验收标准

1. 多轮场景 token 曲线：第 N 轮对 1..N-1 轮结果做 replace 后，prompt token
   不随轮数线性增长（对照曲线留档本文目录）。
2. resume/回放：Load 重建的上下文与中断前可见面一致（编辑序列重放幂等）。
3. 分支继承：fork 出的分支带上祖先分支的编辑；编辑不跨分支泄漏。
4. 旧会话文件零漂移加载；`internal/session` 既有测试全绿。
5. 审计：export/原文始终可见原始条目。

## 6. 规模

C 级粗估：会话层 2 人天 + 工具 1 人天 + 三驱动接线与测试 2 人天。
