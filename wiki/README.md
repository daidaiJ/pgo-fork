# pigo wiki（fork 专属知识库）

> 本目录是本 fork **独有**的 wiki：移植/扩展选题、设计文档、原型深读、工程纪律。
> 与根 `docs/`（发布站点 + issue 档案 + 上游对齐文档）分工：wiki 只放本 fork
> 的自有知识与 dev 分支施工材料。本文件是 L0 唯一入口。

## 1. 按需取用

| 我想要… | 读这个 | 层级 |
|---|---|---|
| 看 dev 分支在移植什么、施工到哪了 | [port/README.md](port/README.md)（选型总表 + 施工顺序 + 交接纪律） | L1 |
| 看候选特性的完整坐标系（六原型交叉选型 + 负面清单） | [port/cross-prototype-selection.md](port/cross-prototype-selection.md) | L1 |
| 施工前确认纪律（编译/测试耗时反例 → R1–R10） | [port/design-principles.md](port/design-principles.md) | L1 |
| 读某个特性的设计细节 | [port/](port/) 下对应分文档（rewind / shellguard / context edit / tool exposure / 流恢复 / 信封 / TUI / 探针） | L2 |
| 深读原型的已有特性与关键机制 | [prototype/](prototype/)（grok-build-proxy 深读等） | L2 |
| 查 harness 能力现状（上游对齐面） | [../docs/harness-capability-matrix.md](../docs/harness-capability-matrix.md)（在 docs/） | 跨目录 |
| 查历史 issue 渲染页 / 在线站点 | [../docs/](../docs/)（issue#*.html 档案 + index.html） | 跨目录 |

## 2. 目录结构

```
wiki/
├── README.md                      ← 本文件（L0 索引，唯一入口）
├── port/                          ← dev 分支移植/扩展专题
│   ├── README.md                  ← L1：选型总表 + 施工顺序 + 交接纪律
│   ├── cross-prototype-selection.md ← L1：五原型交叉选型 + 负面清单
│   ├── design-principles.md       ← L1：工程反例→设计原则（R1–R10，施工必读）
│   └── *.md                       ← L2：各特性设计文档（startup-probes / rewind /
│                                    shellguard / tui / context-edit / tool-exposure /
│                                    stream-recovery / subagent-envelope）
└── prototype/                     ← L2：原型深读笔记（特性设计与关键机制）
```

## 3. 渐进式披露规范（新文档必须遵守）

- **L0（本文件）**：每个文档一行定位 + 一个链接，不展开内容。
- **L1（专题入口）**：状态块（日期/状态/基线）+ TL;DR/选型表 + 指向 L2 的链接。
- **L2（设计/深读文档）**：统一结构 = 状态头（来源/基线/可信度）→ 现状与机制
  （带源码锚点）→ 设计/落点 → 明确不做 → 验收标准 → 规模。每节可独立阅读。
- **L3（附录/台账）**：来源锚点、交接台账、原始调研。按需才读，不在 L2 复述。

**落位规则**：

1. 新特性设计 → `port/<topic>.md`，并在 `port/README.md` 选型表登记一行；
2. 跨特性选型结论 → `port/cross-prototype-selection.md` 对应表加行，不另开文档；
3. 工程纪律 → `port/design-principles.md` 追加条目（只追加，不改写）；
4. 原型深读 → `prototype/<原型名>-<主题>.md`，一个原型一个主题一篇，
   超长拆分时在原型 README 登记分篇索引；
5. 状态变迁（开工/合入/搁置）→ 改文档头状态行，正文历史条目不改写；
6. 禁止在 L2 以下堆"读者第一屏就该看到"的结论——上浮到 L0/L1。

**命名**：小写连字符；living doc 在文头标注"living document"与回填时机。
