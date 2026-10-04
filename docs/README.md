# pigo 文档索引

> 本文件是 `docs/` 的唯一入口（L0）。按"渐进式披露"组织：先一句话定位，再决定
> 要不要往下读。新文档落位与写作规范见 §3。

## 1. 按需取用（读者路线）

| 我想要… | 读这个 | 层级 |
|---|---|---|
| 快速了解 pigo 是什么、怎么装 | 仓库根 [README.md](../README.md) | L0 |
| 看 harness 能力现状与对标基线（哪些已有/缺口/优先级） | [harness-capability-matrix.md](harness-capability-matrix.md)（living doc） | L1 |
| 看沙箱/容器化怎么用 | [sandboxing.md](sandboxing.md) | L1 |
| 看移植/扩展特性的设计（dev 分支施工区） | [port/README.md](port/README.md) | L1 → 分文档 L2 |
| 查历史 issue 的渲染页面 | `issue#00xx.html`（164 份归档导出，在线站点入口 [index.html](index.html)） | L3 档案 |
| 看运行时架构图 / agent loop 工作流 | [pigo-runtime.architecture.json](pigo-runtime.architecture.json) / [pigo-agent-loop.workflow.json](pigo-agent-loop.workflow.json)（配套 [../agent/](../agent/) 源码） | L2 |

## 2. 目录结构

```
docs/
├── README.md                      ← 本文件（L0 索引，唯一入口）
├── harness-capability-matrix.md   ← L1 活文档：能力矩阵与对标基线
├── sandboxing.md                  ← L1：沙箱/容器化
├── port/                          ← L1 索引 + L2 设计文档：移植/扩展专题（dev 分支施工区）
│   ├── README.md                  ← port 入口：选型总表 + 施工顺序 + 交接纪律
│   ├── design-principles.md       ← 工程反例→设计原则（R1–R10，施工必读）
│   ├── cross-prototype-selection.md ← 五原型交叉选型总表（新增候选权威来源）
│   ├── startup-exit-probes.md     ┐
│   ├── rewind-branch-undo.md      │
│   ├── shell-command-analysis.md  │ L2 各特性设计文档
│   ├── tui-crush-components.md    │ （格式统一：状态头 → 现状/缺口 → 设计 → 验收 → 规模）
│   ├── canonical-context-edit.md  │
│   ├── deferred-tool-exposure.md  │
│   ├── stream-recovery-injection.md │
│   └── subagent-result-envelope.md ┘
├── superpowers/specs/             ← 规格存档
├── web/                           ← 在线站点页面（GitHub Pages 构建产物）
├── issue#00xx.html ×164           ← L3 档案：issue 导出（勿手改，由导出流程生成）
├── index.html / CNAME             ← 在线站点入口与域名配置
└── *.json / *.png / install.sh    ← 架构图数据、图片、安装脚本
```

## 3. 渐进式披露规范（新文档必须遵守）

**层级契约**——读者在任何一层都能拿到"够用的信息 + 是否继续读的判断依据"：

- **L0（本文件）**：每个文档一行定位 + 一个链接，不展开内容。
- **L1（专题入口/README）**：状态块（日期/状态/基线）+ TL;DR/选型表 +
  指向 L2 的链接。只回答"有什么、什么状态、去哪读详情"。
- **L2（设计文档）**：统一结构 = 状态头（来源/基线/可信度）→ 现状与缺口
  （带源码锚点）→ 设计/落点 → 明确不做 → 验收标准 → 规模。每节可独立阅读。
- **L3（附录/档案）**：来源锚点、台账、原始调研、生成产物。按需才读，
  不在 L2 复述。

**落位规则**：

1. 新特性设计 → `port/<topic>.md`，并在 `port/README.md` 选型表登记一行；
2. 跨特性选型/对比结论 → `port/cross-prototype-selection.md` 对应表加行，
   不另开新文档；
3. 工程纪律/反例 → `port/design-principles.md` 追加条目（只追加，不改写）；
4. 能力现状变化 → 回填 `harness-capability-matrix.md` 对应行；
5. 状态变迁（开工/合入/搁置）→ 改文档头状态行，正文历史条目不改写；
6. 禁止在 L2 以下的文档里堆"读者第一屏就该看到"的结论——上浮到 L0/L1。

**命名**：小写连字符（`topic-name.md`）；台账/矩阵类 living doc 在文头标注
"living document（活文档）"与回填时机。
