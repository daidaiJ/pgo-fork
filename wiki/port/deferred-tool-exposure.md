# P1 设计原型：延迟工具声明（三档 exposure + 按名认领 + 能力门）

> 来源：grok-build-proxy v1.0.41 已落地 allowlist 全链路（T1）+ 定稿设计（T2，
> 三参照合并：pi tool_search/exposure + kimi 公告流/capability 门 + step 无参照）。
> 原始设计 `docs-local/workflow-pi-port/deferred-tool-exposure.md`（含 pi 0.99.2/1.0.0
> 三条迭代修正）。本文是其面向 pigo 的调整版。
> pigo 侧现状为本机核实（2026-10-04）。

## 1. 问题

pigo 声明给模型的工具面 = 全量内置工具 + MCP 工具。工具面 token 成本随
MCP 接入线性上涨，且大工具面推高误用率；对国产供应商（DeepSeek/GLM/Kimi
按缓存计费）声明面瘦身还有直接成本收益。

现有准入控制（`--allowed-tools/--disallowed-tools`，`internal/cli/run/toolpolicy.go`
的 `ApplyToolPolicy`）是**静态裁剪**：工具要么在要么不在，没有"不声明但可发现"
的维度。

## 2. 设计定稿（三参照合并结论，照单全收）

### 2.1 exposure 收敛三档

| 档 | 语义 |
|---|---|
| `direct` | 声明给模型（现状行为，默认） |
| `deferred` | 不声明；模型经 `search_tools` 按名认领后下一轮进声明面 |
| `hidden` | 不声明、不可发现（保留连通性，如后台 MCP server） |

不做 pi 的 `codemode`/`model-only` 档（pigo 无第二脚本运行时，与 pi 上游
D1 决策一致）。

### 2.2 发现机制 = 公告流 + 按名认领，不上 BM25

- deferred 工具以**名字清单公告**进会话（`<system-reminder>` 注入形态，
  pigo 已有该形态——harness 矩阵 US-002 落地的动态注入机制），公告携带
  server/工具 description 一行式摘要（让模型"知道往哪认领"）。
- `search_tools(query)` 内置工具：名称精确/前缀 + description 包含匹配打分，
  命中者下一轮进声明面；未命中返回空集不报错。BM25 留接口位，实测不够再升。
- **只增不减**：声明态无移除入口，不做 `tools_removed`。
- **静态描述约束**（pi 0.99.2 #10212）：公告清单与 `search_tools` 自身描述
  一次性定型，不随 MCP 连接状态变化；易变信息走"变更才追加"的独立段落
  ——prompt cache 友好。

### 2.3 能力门（国模适配硬约束）

只有具备动态加载工具能力位的模型走 deferred；其余回落 direct 全量声明并打
info 日志——"按能力裁剪请求，而不是发出去等上游报错"（kimi
`dynamically_loaded_tools` 语义）。pigo 侧能力位载体：`internal/provider`
模型能力位（对齐现有 ThinkingLevel 的 per-model 配置模式），默认 false 回落
direct；BYOK/自定义模型在配置里显式打开。

### 2.4 声明态持久化

声明记录在**会话内**（编辑类条目或独立声明条目，复用
[canonical-context-edit.md](canonical-context-edit.md) 建立的 Version 迁移
机制）——resume/分支不丢不串。**恢复原子性**：resume 时声明态与工具面重建
必须同批完成，不能出现"声明态在、工具没挂上"的中间态（pi 1.0.0 修过的
反面教材）。

## 3. 落点（pigo 侧）

1. **配置**：MCP server 配置与工具注册面增加 `exposure`（server 级默认 +
   工具级覆盖，读取时归一）；`config.toml` 新增
   `tools.declaration_mode = "direct" | "deferred"` 全局项（默认 direct；
   来源项目用户拍板项照搬）。
2. **声明面过滤**：`ApplyToolPolicy` 输出后接"声明态过滤"层——注册不裁剪、
   每轮构建工具面时按声明态过滤（落点 `internal/runtime` 每轮 `TurnUpdate`
   的 tools 构建，或 `run.SetupEnv` 组装 + loop 内过滤，施工时按
   `internal/runtime/loop.go` 现有 seam 定）。
3. **`search_tools` 内置工具**（`internal/agenttool/search_tools_tool.go`）：
   只在声明态有 deferred 集时注册；认领动作更新会话内声明态 + 下一轮公告。
4. **子代理面**：task 子代理（`subagent_rpc`）透传 allowlist（已有
   `ChildToolSet`，`toolpolicy.go:212`）+ `defer_tools` 开关，语义与来源
   T1 一致：defer 时保留面 = allowlist，其余记入 deferred 集；能力只减不增。
5. **grok 绑定剥离**：来源的 workflow payload/hash/divergence 机制全部不搬
   （pigo 无 Rhai journal）；`SamplerConfig` 载体换成 pigo 模型能力位。

## 4. 验收标准

1. 声明面瘦身：defer 档首 prompt 工具面 token 相对 direct 档可量化下降
   （MCP 挂 3 server ≥30 工具对照），任务完成率不劣化。
2. `search_tools` 命中 → 下一轮可调用；未命中返回空集不报错。
3. 能力门：无能力位模型在 deferred 配置下回落 direct 全量声明，日志可见，
   deferred 工具不进请求面。
4. resume：经 search_tools 认领的工具在会话恢复后仍可直接调用（不重搜）。
5. 权限交集：tool policy deny 的工具即使被认领也不进声明面（deferred 不放大权限）。
6. 公告流轻量且变更才追加（cache 友好断言：连续两轮无认领时无新公告条目）。

## 5. 规模

C 级粗估：配置与声明态 2 人天 + search_tools 工具 1 人天 + 声明面过滤与
resume 原子性 2–3 人天 + 子代理透传 1 人天。
