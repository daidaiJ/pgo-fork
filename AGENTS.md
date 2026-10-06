# pigo-fork AGENTS.md（agent 会话入口）

> 本文件是 fork 仓库的 agent 常驻上下文：只保留 handoff 摘要指针，详细知识全部
> 在 wiki（渐进式披露，L0 入口见 `wiki/README.md`）。

## Handoff 摘要指针（每阶段收口必须更新本节）

- **当前阶段**：**T4.2 questionnaire 结构化提问落地（2026-10-06，第 4 期实作面
  全部收口）**——多步问卷 schema + `ask_user` 工具 + TUI 问题面板：内核 =
  `internal/questionnaire` 叶子包（minimax V2 schema 子集：1..4 步/每步 0..4
  选项/recommended 唯一/single·multiple/allow_other/显式 skip + Normalize/
  DegradedReply/ResultText）+ `agenttool.AskUserTool`（**阻塞式 QuestionPort
  端口**，zcode/kimi 形态而非 minimax 停轮模型，D-1；sequential 执行；nil
  端口降级 = 自动答 recommended→首选项、`requires_explicit_response` 给
  "state your assumption" 指引，D-3）；问答经普通 role=tool 条目入史。
  接线 = `run.SetAskPort` finder + REPL stdin 端口（ConfirmMu + trust 同款
  安全论证，Enter=recommended）+ TUI `teaAskPort`/`askPanel`（单步向导、
  Warn 统一等待徽章、恒一个 waitAsk 在途）+ task 子代理剔除 ask_user（D-4）。
  偏差 D-1~D-8 登记
  [questionnaire-ask-user.md](wiki/port/questionnaire-ask-user.md) §7。
  基线 **39 包 0 FAIL**（`.gtmp/t42-full.txt`）。
  此前 T4.3（`4b84390`，BTW peek 会话 + uniqueID 根治既有 flake）、
  T4.1（`58aedec`）、T3.3.1（`1768d08`）、T4.4 均清——**第 4 期
  T4.1/T4.2/T4.3/T4.4 实作面全部 ✅**。
  **第 5/6 期重排（2026-10-06，用户要求按重要性×收益拆分）**：依据 =
  [wiki/port/phase5-split-assessment.md](wiki/port/phase5-split-assessment.md)
  （双轴 AX/CX 评分矩阵 + 四条现状纠偏 + 降级台账）。新第 5 期 =
  **agent 自主性与决策质量**（T5.1 子代理信封 / T5.2 副作用契约+权限规则层
  打包 / T5.3 内置 skill 双件 / T5.4 多模型路由评估 / T5.5 供应商韧性 /
  T5.6 截断挽救）；新第 6 期 = **生态耐久**（T6.1 会话租约 / T6.2 MCP
  先评估（pigo 无 client，原 2d 估漏地基）/ T6.3 usage 缩 MVP / T6.4 pstack
  只盘点 / T6.5 候选池）。
  **下一步待用户拍板**：第 4 期收口验收（T4.1 期验收 1 defer 档 token A/B
  仍未做，需 API key 可随时插入）；T5.5/T5.6 是否进第 5 期、T6.2 MCP 是否
  补 client、T6.5 候选池排序；其余待拍板项
  （pi-durable 切片 1、/ui 面板、skill-as-tool、
  供应商韧性 S 件族、dynamic-workflow、code mode、TUI 风格对齐 grok——
  tui-grok-style.md 14 条 S 需求待排期）见 implementation-plan 待议段；
  其余均见 handoff。
- **交接台账（L0.5 必读，2026-10-05 起瘦身版）**：
  [wiki/port/handoff.md](wiki/port/handoff.md) 只保留**最新一条交接卡**，
  历史条目在 [wiki/port/handoff-archive.md](wiki/port/handoff-archive.md)
  ——新会话先读最新卡，再 `git log` + `git status` 核对现场，禁止凭记忆
  续写；更早脉络查 archive。
- **施工权威**：[wiki/port/implementation-plan.md](wiki/port/implementation-plan.md)
  （六期任务表，状态每期收口回填；第 1 期已清空）。
- **施工纪律**：[wiki/port/design-principles.md](wiki/port/design-principles.md)
  （R1–R11，R11=多参照实现择优：能融合则融合、不能融合取相对最优并在规格
  登记互斥原因；附录已登记 7 个 Windows 测试差异族 + `internal/testgate`
  门控机制，
  本机全量测试基线 = 门控后全绿）。
- **T1.1 规格**：[wiki/port/startup-exit-probes.md](wiki/port/startup-exit-probes.md)
  （span 名以 §2.2 埋点表为唯一权威，已含实现偏差登记）。

## 环境事实

- `wiki` 是指向 `D:\wiki\pandawiki\projects\pgo-fork\wiki` 的符号链接，已在
  `.gitignore`（`/wiki`）——**wiki 内容不进 git**，改 wiki 即改 pandawiki 侧文件。
- Windows 开发机：`go build` 间歇被卡巴斯基锁 `%TEMP%`，构建统一加
  `GOTMPDIR=<repo>/.gtmp`（R9）——**只用于 go build**：go test 不受卡巴
  斯基影响，加了反而让 `t.TempDir()` 走 GOTMPDIR 产出混合斜杠路径打破
  trust 测试；全量测试后台跑、输出落文件（R6）。
- dev 分支本地开发，推送/PR 策略待用户拍板。
