# 设计原则：从 grok-build-proxy 的编译/测试耗时反例提炼

> 来源：grok-build-proxy `AGENTS.md`「调试时间成本原则」（§10，八条）+
> `docs-local/port-roadmap.md` §6 交接台账里的实战教训。这些是 Rust workspace
>（102 crate）上烧出来的反例；本文逐条转成 pigo（Go）的规则——**大部分照搬，
> 少数因 Go 编译快而反转，全部变成可执行的设计约束**，实现时不得重蹈覆辙。

## R1 全量测试是最贵操作，验证分层：check → 窄过滤 → 相关包 → 全量

**反例**（grok）：全量 `cargo test -p xai-grok-pager --lib` ≈ 编译 6–11 分钟 +
跑 4 分钟；"改一行 → 全量 → 再改一行"的循环被明令禁止。

**pigo 规则**：Go 编译快，但大包测试二进制链接与全量 `go test ./...` 依然以
十秒–分钟计（pigo 约 200 个测试文件）。施工节奏固定为：

1. `go build ./internal/<pkg>/` + `go vet ./internal/<pkg>/`（语法/类型，秒级）；
2. `go test -run <TestName> ./internal/<pkg>/`（行为，窄过滤）；
3. `go test ./internal/<pkg>/...`（相关包）；
4. `go test ./...`（一轮收口，**提交前必跑**）。
   一次改动批量收集信息后只跑一轮，禁止逐个盲修。

## R2 高扇入包 = 高重编面，新特性落点优先"叶子包"

**反例**（grok）：改共享 crate（config/agent）级联重编整棵依赖树；
deferred-tool-exposure 施工时动 `xai-grok-agent` builder/config 触及整个依赖树，
文档明文要求"先 `cargo check -p` 验证再进测试"。

**pigo 规则（结构性的，进各特性设计文档）**：

- 新能力优先落成**叶子包**（`internal/spans`、`internal/shellguard` 这类零内部
  依赖的纯库），通过 seam（`RunConfig` / `run.SetupEnv`）接线，不改
  `agentcore`/`runtime` 主干；
- 每份特性设计文档须列"级联影响面"：改到 `agentcore`（所有工具/所有测试的
  依赖根）或 `internal/session`（三驱动共用）时，显式声明并预算验证时间；
- 施工顺序按依赖叶 → 根，不倒挂。

## R3 测试过滤器省跑不省编，且过滤器有覆盖率陷阱

**反例**（grok，CI 红灯教训）：`--lib rewind` 过滤跑了整个开发周期，但两个
测试用例名不含 "rewind"，从未被过滤执行过；Linux CI 全量首跑才暴露，
tag 已发出被迫重指。另外过滤器仍要整编测试二进制，"选重编级联最小的 crate
跑过滤"是显式要求；新套件先跑基线再谈回归。

**pigo 规则**：

- 本地窄过滤（`-run`）只是调试手段，**合入前必须整包跑**（R1 第 4 层）；
  全量在 CI 每次都跑——本地过滤永远不能替代 CI 绿；
- 新测试命名必须与其主题一致（测试名是过滤器的输入）；主题过滤跑过的用例
  ≠ 全绿，两者差集就是风险集；
- 新增测试目录/包首跑记录基线（数量、耗时），后续回归对照。

## R4 夹具与持久真相必须一致

**反例**（grok，T2b 回退教训）：replay 统一路径的锚点是"journal 即真相"，
但测试夹具只播种内存对话、journal 文件为空 → replay 重建自然清空，6 用例
CI 全红，整个特性被迫回退出 main。根因是语义锚点冲突，不是实现 bug。

**pigo 规则**：会话相关测试的夹具一律**从 `Store.Save`/`AppendBranch` 真实
写出 session JSONL** 再用 `Store.Load` 读回断言，禁止手写 JSONL 行或在内存
拼 MessageList 冒充持久态——被测语义以哪个为真相，夹具就必须从哪个生成。

## R5 平台差异族统一登记，不允许逐个打地鼠

**反例**（grok）：Windows 上 25 个挂测试全是 `AbsPathBuf("/tmp")` 同一环境族，
靠 `win-skip.txt` + ctest 白名单统一 skip + `WIN-TEST-GATE.md` 分诊流水线治理；
没有这套东西时曾逐个排查烧大量时间。

**pigo 规则**：平台性失败先分族（路径形态/信号/锁/fork），一族一策：
- 同族用 `t.Skip` 带统一前缀注释（`// pigo:win-skip(<族名>): <根因>`），
  族名登记在本文档 §附录，禁止无根因的裸 skip；
- 上游式整包平台回归（race/CGO 等）走 WSL：`wsl -e bash -c "cd ... && go test -race ./..."`；
- 本机 Windows 验证默认不带 race（链接慢），race 留给 CI/WSL。

## R6 长任务后台跑 + 输出不接截断管道

**反例**（grok）：编译/测试输出接 `tail/head` 管道 → 失败时断言详情被丢弃，
只能整轮重跑，时间成本翻倍；阻塞死等长任务同样是明令禁止项。

**pigo 规则**：全量测试/构建放后台（输出落文件），完成后对**日志文件**做
tail/grep 取详情；禁止前台死等 + 管道截断的组合。

## R7 A/B 对照的适用边界按语言成本定

**反例**（grok，2026-10-03 定）：v1.0.41 一次"修前 A/B + 修后验证"两轮近全量
shell 测试编译即反例——Rust 上"改动前先跑一遍原始版"烧一整轮重编不值。

**pigo 规则（反转条款）**：Go 是快编译语言，**修前 A/B 基线跑是允许且推荐的**；
仍然禁止的是为"对照"单独烧多轮全量——基线并入修后验证的同一轮计划里排布。
本条与来源项目相反，是有意为之。

## R8 验证绿一块提交一块；中断恢复先核对现场

**反例**（grok 交接台账多起）：跨会话施工靠记忆续写出过"合并后 main 编译不过"
（调用点漏补参）、tag 发出后才发现 CI 红。来源项目的交接协议（§5：完成/中断
必须在台账追加条目，接手者先 `git log` + `git status` + diff 核对，禁止凭记忆
续写）是为此设计的。

**pigo 规则**：
- 每片验证绿（R1 第 4 层 + `go vet`）才 commit，commit message 带验证结果摘要；
- dev 分支跨会话施工：接手先核对现场再续作，进度记录在对应特性文档的
  「状态」行（只追加，不改写历史条目）；
- 主干（master）合并前 CI 必须绿，**不以"本机过了"为由跳过**——tag/发版
  一旦带红，回收成本远高于等待（grok v1.0.41/42 两次 tag 重指的教训）。

## R9 本机环境陷阱登记（Windows 开发机）

- 卡巴斯基锁 `%TEMP%` 导致 `go build` 间歇 `Access is denied`：构建走
  `GOTMPDIR=<repo>/.gtmp`（已 gitignore）；`go test`/`go vet` 不受影响。
- 构建产物（`.gtmp/`、coverage 文件）不进库，`.gitignore` 维护到位。

## R10 测试编译单元最小化——防"测一个小特性也要全 lib/全 CLI 级编译"

**反例**（grok，用户明示的重大耗时项）：测一个小特性也要全 lib 甚至全 CLI
编译测试——测试二进制把整个 crate（乃至 CLI 入口依赖树）链在一起，任何一处
改动都触发大范围重编重链，小验证被大编译绑架。

**pigo 规则（设计期约束，比施工期更重要）**：

- 单元测试与被测代码**同目录同包**（pigo 既有约定），测试 import 面只进被测
  包及其直接依赖，**禁止单元测试 import `cmd/` 或整棵 `internal/cli` 组装树**；
- 跨层集成测试集中放在薄的集成测试文件/目录（复用 `internal/runtime`
  `faux_provider_test.go` 的假 provider 模式驱动 loop，不起真实 provider），
  且数量刻意少——集成测试是回归网，不是调试手段；
- 新包的测试文件从第一天保持独立可编译（`go test ./internal/<pkg>/` 秒级），
  这是 R2"叶子包"落点的好处之一：叶子包的小验证永远不被大组装树绑架；
- 出现"改 A 包却必须重编 B 大包才能测"的信号时，先修依赖结构（加 seam /
  收窄接口），再把测试写下去。

**原型测试用例移植**：minimax-code（vendor 了 pi 的测试体系 + 自有模块级
单测）、qwen-code（`loopDetectionService`、truncation、sessionService
corruption 等专项测试）、ZCode（turn-state 合法迁移表等）都带现成测试用例，
各特性施工时**优先改编原型对应测试**（语义已验证），不凭空新写。

## 附录：平台差异族登记（只追加）

| 族名 | 根因 | 处置 |
|---|---|---|
| （暂无，出现即登记） | | |
