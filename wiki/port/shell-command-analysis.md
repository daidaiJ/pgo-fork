# P0 设计原型：bash 命令静态安全分析（三态判定 + 无人值守收口）

> 来源：grok-build-proxy（`xai-grok-workspace/src/permission/`，v1.0.41 含
> headless 非交互收口；语义源 Step-Code `shell-analysis.ts`/`command-policy.ts`）。
> 原始调研 `docs-local/step-code-port/survey.md` P1 节。本文是其面向 pigo 的调整版。
> pigo 侧现状为本机核实（2026-10-04）。

## 1. 问题

pigo 的 bash 权限模型停在**工具级准入**：`--allowed-tools/--disallowed-tools`
（`internal/cli/run/toolpolicy.go`）决定 bash 工具是否可用，但一旦放行，
**命令内容本身不做任何静态检查**——`rm -rf /` 与 `ls` 在模型生成命令的层面无差别。
hooks（PreToolUse）是外部脚本通道，没有内置判定。

廉价国模 + 无人值守（headless/长任务）是 pigo 的主场景之一；命令生成可靠性弱于
一线模型时，命令内容的静态安全下限恰是最缺的一块。

## 2. 来源实现要点（已发版，语义可照搬）

grok fork 在 2026-09-23 上游同步后获得的全套（`xai-grok-workspace/src/permission/`），
四件套缺一不可：

1. **typed 解析而非正则**：`bash_command_splitting.rs` 用 tree-sitter-bash 解析，
   wrapper/transparent-prefix 保守剥离（`bash -ec -- 'rm -rf x'` 解到内层真实命令、
   `timeout/nice/stdbuf` 有显式 arity，不支持项不猜）。
2. **危险命令规则**：`grants.rs` `is_dangerous_command_words`——词边界前缀匹配
   rm/chmod/chown/chattr/pkill/kill/git push 等，**白名单也不能覆盖**这些。
3. **风险面分析**：`exec_risk.rs` spawn 类 argv 旗标、git 只读操作单一事实源、
   环境配置扫描。
4. **三态判定 + fail-closed**：`GateDecision::{Reject, AskRuleMatch, AskFailClosed}`，
   **"解析不了"不冒充"安全"**（`analysisIncomplete` 是一等公民）——解析错误、
   未验证边界、预算耗尽 → 未决；未决 ≠ 危险，但同样不得自动放行。
   加 headless 收口：非交互遇审批阻塞默认终止，opt-in
   `--non-interactive-denial continue` 转成失败工具结果让 agent 换路续跑
   （v1.0.41 新增，PATCHES 十八期）。

## 3. 落点（pigo 侧）

1. **新包 `internal/shellguard`**（纯库，无 IO 依赖，便于测试）：
   - 解析器选型：Go 侧无成熟 tree-sitter-bash 绑定优势，首版用
     **词法级分析**（shellwords 展开 + wrapper 命令表 + 危险词规则 + 引号/
     heredoc/替换边界的保守标记）——把"无法确信解析正确"显式标为
     `analysisIncomplete`，不冒充安全。这与来源项目"三态语义比解析器本身值钱"
     的结论一致；tree-sitter 升级留作后续（cgo 依赖另行评估）。
   - API：`Analyze(cmd string) Decision`，`Decision::{Safe, Hazardous(findings), Incomplete(reason)}`。
   - 规则表参照来源 `grants.rs` 危险词清单 + `exec_risk.rs` 旗标清单起库，
     逐条登记来源（可审计）。
2. **接线点 `BeforeToolCall` seam**（`internal/runtime/loop.go` 已有该 seam）：
   bash 工具执行前跑 `shellguard.Analyze`，`Hazardous` → 拒绝或转审批（REPL/TUI
   有交互通道时 Ask；headless 走第 4 条）；`Incomplete` → 不自动放行，同样走
   审批/拒绝。与 hooks PreToolUse 的边界：shellguard 是**内置默认防线**，
   hooks 是用户自定义通道，二者都跑、任一 deny 即 deny。
3. **配置项**：`config.toml` 新增 `[shellguard] mode = "off" | "ask" | "strict"`
   （默认 ask；off 仅限显式关闭，文档标注风险）。走
   `internal/cli/config.FileConfig` → `applyFileConfig`（flag > file > default 约定）。
4. **headless 收口**：headless 模式遇 `Hazardous/Incomplete` 且无审批通道时默认
   deny 终止；新增 opt-in flag `--non-interactive-denial continue` 把可恢复的
   阻塞转成失败工具结果（文本说明原因），agent 可换路续跑；不可恢复的执行环境
   故障仍终止。落点在 headless 驱动层（`internal/cli/headless`）。
5. **grok 绑定剥离**：来源实现无 xAI 绑定，全部可搬；规则表不搬
   `security_findings` 喂 auto-mode 分类器的部分（pigo 无对应分类器，v1 不引入）。

## 4. 验收标准

1. 主验收场景 = headless + 非一线模型驱动 bash：危险命令（rm -rf、git push --force、
   chmod 777、pkill）100% 拒绝或收口；`bash -ec -- '...'` 内层命令、
   `timeout 10 rm -rf x` wrapper、引号内伪装（`echo "rm -rf"`）不误报不漏报
   （表驱动用例，来源 `grants.rs` 规则逐条映射）。
2. `Incomplete` 不冒充安全：解析失败的输入走审批/deny 路径，绝不自动放行。
3. `mode=off` 时零开销路径（不做分析）；ask 模式交互审批可放行并记录。
4. `--non-interactive-denial continue`：阻塞转失败工具结果，agent 换路成功续跑
   一个用例；默认档保持终止行为。
5. `go test ./internal/shellguard/...` + 既有 runtime loop 测试不回归。

## 5. 规模

C 级粗估：shellguard 包 3–5 人天（规则表是主要工作量），接线 + headless 收口 1–2 人天。
