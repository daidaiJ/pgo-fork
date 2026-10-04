# 随手件：子代理结果信封 + next_step 映射

> 来源：grok-build-proxy 预研定稿（kimicode-port P3，未施工，路线图随手件）；
> 语义源 kimi-code `formatForegroundAgentSuccess`（`agentTool.ts:756-774`）。
> 本文是其面向 pigo 的调整版。

## 1. 问题

pigo 的 task 子代理（进程隔离 JSON-RPC + 进度事件，`internal/runtime` task 面）
返回给父代理的是最终文本结果。缺口：

- 停止原因（max tokens/max steps/用户取消/异常）不结构化——弱模型无法据此
  决定下一步，只能自由发挥；
- 失败的子代理只能重派全新 agent 白烧 token，没有"同一子会话续跑"语义。

## 2. 设计（kimi 信封语义）

父代理收到的成功结果格式化为结构化信封：

```
agent_id / status / stop_reason / [summary] / resume_hint / next_step
```

关键映射：**每个 stop_reason 映射到不同的 next_step 指令**——

| stop_reason | next_step 语义 |
|---|---|
| `max_tokens` | 建议拆小任务或续跑（resume_hint 带上 agent_id） |
| `max_steps` | 任务未完成，给出已完成部分摘要 + 续跑建议 |
| `user_cancel` | 明示被取消，不暗示继续 |
| `completed` | 正常交付（final message 即唯一 handoff） |

失败时可 `task(resume=<agent_id>)` 复用同一子会话续跑（子会话是持久实体），
而非重派全新 agent。

配套两条 kimi 约束：

- **final message 即唯一 handoff**：父只收最后一条 assistant 文本，中间过程
  留在子代理自己的上下文；后台任务输出超限时截尾只回 preview。
- **委托图约束**（P2 可选）：能派 agent 的 profile 自动从别人的委托 allowlist
  剔除，声明层保证委托链终止——比运行时深度计数干净（pigo 已有嵌套防护，
  此条为声明层增强，登记可选）。

## 3. 落点（pigo 侧）

1. **信封格式化**：task 工具结果组装处（`internal/agenttool/task` 或
   `internal/runtime` task 面，施工时锚定）把 stop_reason/agent_id/summary
   格式化进返回文本（JSON 或结构化文本块，弱模型友好优先选文本 + 固定字段行）。
2. **stop_reason 采集**：subagent_rpc 模式的进度/终止事件补 stop_reason 字段
   （JSON-RPC 协议加字段，向后兼容）。
3. **resume 通道**：task 工具新增 `resume` 参数——pigo 子代理会话若已持久化
   （对齐会话存储），resume = 加载该子会话 + 续跑；若 v1 子会话未持久化，
   先只做信封 + next_step 映射，resume 登记为二期。
4. **验收定位**：按"弱模型补强"验收——结构化契约替代弱模型自由判断。

## 4. 验收标准

1. 各 stop_reason 逐条映射正确的 next_step（表驱动用例）。
2. 信封字段齐备（agent_id/status/stop_reason/resume_hint），父代理可据
   resume_hint 发起续跑（若二期 resume 落地）。
3. 正常完成路径信封不干扰既有结果消费（SDK examples 不回归）。

## 5. 规模

C 级粗估：信封 + 映射 1 人天；协议字段 0.5 人天；resume 通道（二期）2–3 人天。
