# P0 设计原型：rewind 分支树 undo（弃分支可往返 + prompt 放回 + 边界预计算）

> 来源：grok-build-proxy v1.0.41 已发版实现（2026-10-03，合 main）。
> 原始设计文档 `docs-local/kimicode-port/rewind-branch-undo.md`（语义源 kimi-code
> `undoService` + Step-Code branch summarization）。本文是其面向 pigo 的调整版。
> pigo 侧锚点为本机核实（2026-10-04）。

## 1. pigo 现状（A 级，已核实）

pigo 的会话树存储已经走在正确的路线上，比来源项目当初的起点更靠前：

- **非破坏树存储已有**：`internal/session/session.go:637` `AppendBranch`——
  在 parentLeafID 下追加链式 entry，**保留所有已有 entry 与兄弟分支**；
  `Append` 才是抹平历史的路线。REPL（`internal/cli/persist.go:46`）、
  headless（`internal/cli/headless/session.go:180`）、TUI
  （`internal/cli/tui/session.go:466`）全部用 AppendBranch 增长树。
- **/rewind 已有**（`internal/cli/repl/rewind.go`）：`/rewind` 列文件快照恢复点
  → `snap.Restore(n)` 回滚 write/edit 文件变更（`internal/agenttool/file_snapshot.go:157`）
  → `rewindConversation` 换活跃叶子（`LoadEntries` + `PathToLeaf` 重建上下文）。
  与 Claude Code 的 Esc-Esc 对标；bash 造成的文件变更不捕获（v1 边界，doc comment 明示）。
- **/tree 已有**：纯叶子导航，重建 root→leaf 路径消息。
- **schema v3**：`SessionHeader` 含 parentId 树结构，`Store.Load` 可整树还原。

结论：**kimi 的 SwitchEdge"追加式分支标记"在 pigo 侧由树存储 + 叶子指针天然实现**
——弃分支的 entry 物理保留、切回 = 把叶子指回弃分支。真正缺的是体验层四件事。

## 2. 缺口清单

| # | 缺口 | 来源项目对应实现（Rust 锚点） |
|---|---|---|
| G1 | 弃分支的恢复点在 `/rewind` 列表中**不可见**——恢复点来自内存态 `FileSnapshotRecorder.Points()`（`file_snapshot.go:138`），rewind 换叶子后旧分支的点不再出现；切回只能靠 /tree 盲选叶子 | `get_rewind_points` 从 journal 分支面推导 + picker 标注弃分支（`acp_session_impl/rewind.rs:25`，T1-T3 已实现 ↩/⚠ 标注） |
| G2 | rewind 后**原 prompt 不放回输入框** | `prompt_text` 放回 + regeneration/edit_and_retry 遥测（`dispatch/rewind.rs:396`） |
| G3 | **compaction 边界不预计算**：恢复点列表不标注"该点早于最近一次 compaction，回放将有损"，用户选中后才发现结果被摘要折叠 | `ForkLineError('compaction_boundary')` 预计算进 picker（`replay.rs:372` lossy 语义） |
| G4 | **TUI 无 /rewind 入口**（仅 REPL 有） | pager picker 全链路（`views/rewind.rs`） |
| G5 | 文件快照只覆盖 write/edit，bash 改动不捕获（与来源项目"文件半边不动"的边界一致，登记不立项） | 同左（kimi 自己的 /undo 也不回滚代码） |

## 3. 落点与设计（pigo 侧）

1. **恢复点从树推导（G1）**：`/rewind` 列表改为"活跃分支全量 turn 点 + 弃分支点
   （标注 ↩）"。数据源用 `Store.LoadEntries` + 树遍历（每个分支点 = 一个有
   UserMessage 的 entry），文件快照数从 `FileSnapshotRecorder` 按 turn 关联；
   选中弃分支的点 = 把 `curLeaf` 指向该 entry（AppendBranch 语义天然支持，
   `internal/cli/cli.go` PersistTurn 无需改）。**不引入新 journal 事件类型**——
   这是与来源项目最大的简化：grok 侧需要 RewindMarker SwitchEdge 化是因为它的
   持久层是线性 journal + 折叠回放，pigo 的树存储已内置分支，语义等价物免费。
2. **prompt 放回（G2）**：REPL `/rewind N` 成功后把该点 UserMessage 文本填回输入
   行（REPL 行编辑器 + TUI `input.go`）；REPL/TUI 共用实现放
   `internal/cli/cli.go`（与 PersistTurn 同层）。"改了再发"的遥测区分
   （regeneration vs edit_and_retry）v1 不做，登记。
3. **边界预计算（G3）**：恢复点标注"早于最近 compaction"——从会话 entry 序列找
   最后一个 compaction 摘要 entry（`internal/compaction` 落点），早于它的点在
   列表标 ⚠ 并注明"回退将基于压缩摘要重建"。选中后的行为 = 以摘要为基线上溯，
   与现有 `Store.Load` 回放一致即可，不要求逐字还原。
4. **TUI 入口（G4）**：`internal/cli/tui` 增加 /rewind picker（复用 slash 命令
   分层注册，`internal/runtime/slashcommand.go`；UI 沿用 TUI 现有列表组件，
   组件层改造见 [tui-crush-components.md](tui-crush-components.md)）。

## 4. 明确不做

- 不新建 journal 事件类型 / 不改会话文件 schema（树存储已覆盖）。
- 不回滚 bash 造成的文件变更（与来源项目边界一致）。
- 不做"离开分支自动摘要"（Step-Code branch summarization）：pigo 的
  compaction 已有摘要通道，等 ①③ 落地后按需评估。

## 5. 验收标准

1. rewind → 编辑重发 → 再 rewind 切回弃分支：会话树中两条分支 entry 均完整
   （grep 断言 session JSONL），上下文重建正确。
2. 弃分支的点出现在 `/rewind` 列表且可选中切回（G1）。
3. rewind 后原 prompt 出现在输入框（G2），TUI 与 REPL 一致。
4. 早于 compaction 的点带 ⚠ 标注，选中不 panic、摘要基线重建可用（G3）。
5. 旧会话文件（v3）加载零漂移；`internal/session` 全部既有测试不回归。

## 6. 规模

C 级粗估：REPL 面 1–2 人天，TUI picker 1–2 人天，边界标注 0.5 人天。
